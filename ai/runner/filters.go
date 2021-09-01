package runner

import (
	"math"

	"github.com/domino14/macondo/alphabet"
	"github.com/domino14/macondo/config"
	"github.com/domino14/macondo/gaddag"
	"github.com/domino14/macondo/game"
	pb "github.com/domino14/macondo/gen/api/proto/macondo"
	"github.com/domino14/macondo/move"
	"github.com/rs/zerolog/log"
)

// Note: because of the nature of this algorithm, the lower these numbers, the
// more time the bot will take to find its move.
var BotConfigs = map[pb.BotRequest_BotCode]struct {
	baseFindability     float64
	parallelFindability float64
	isCel               bool
}{
	pb.BotRequest_LEVEL1_CEL_BOT:       {baseFindability: 0.2, parallelFindability: 0.25, isCel: true},
	pb.BotRequest_LEVEL2_CEL_BOT:       {baseFindability: 0.5, parallelFindability: 0.5, isCel: true},
	pb.BotRequest_LEVEL3_CEL_BOT:       {isCel: true},
	pb.BotRequest_LEVEL4_CEL_BOT:       {isCel: true},
	pb.BotRequest_LEVEL1_PROBABILISTIC: {baseFindability: 0.07, parallelFindability: 0.1, isCel: false},
	pb.BotRequest_LEVEL2_PROBABILISTIC: {baseFindability: 0.15, parallelFindability: 0.2, isCel: false},
	pb.BotRequest_LEVEL3_PROBABILISTIC: {baseFindability: 0.35, parallelFindability: 0.45, isCel: false},
	pb.BotRequest_LEVEL4_PROBABILISTIC: {baseFindability: 0.6, parallelFindability: 0.7, isCel: false},
	pb.BotRequest_LEVEL5_PROBABILISTIC: {baseFindability: 0.85, parallelFindability: 0.85, isCel: false},
}

func filter(cfg *config.Config, g *game.Game, rack *alphabet.Rack, plays []*move.Move, r float64, botType pb.BotRequest_BotCode) *move.Move {

	passMove := move.NewPassMove(rack.TilesOn(), g.Alphabet())
	botConfig, botConfigExists := BotConfigs[botType]
	if botConfigExists {
		filterFunction := func([]alphabet.MachineWord) (bool, error) { return true, nil }
		// Only apply CEL filters to english lexica
		if botConfig.isCel && g.Alphabet().Name() == alphabet.AlphabetNameEnglish {
			gd, err := gaddag.GetDawg(cfg, "ECWL")
			if err != nil {
				filterFunction = func([]alphabet.MachineWord) (bool, error) { return false, err }
			} else {
				lex := gaddag.Lexicon{GenericDawg: gd}
				// XXX: There might be a slick way to consolidate this
				// stufilterFunction using generic function pointer types and casting
				// but I'm not sure. This is probably good enough
				if g.Rules().Variant() == game.VarWordSmog {
					filterFunction = func(mws []alphabet.MachineWord) (bool, error) {
						for _, mw := range mws {
							if !lex.HasAnagram(mw) {
								return false, nil
							}
						}
						return true, nil
					}
				} else {
					filterFunction = func(mws []alphabet.MachineWord) (bool, error) {
						for _, mw := range mws {
							if !lex.HasWord(mw) {
								return false, nil
							}
						}
						return true, nil
					}
				}
			}
		}

		// LEVEL4_CEL_BOT is an unfiltered CEL bot
		if botType != pb.BotRequest_LEVEL4_CEL_BOT {
			dist := g.Bag().LetterDistribution()
			// XXX: This should be cached
			subChooseCombos := createSubCombos(dist)
			filterFunctionPrev := filterFunction
			filterFunction = func(mws []alphabet.MachineWord) (bool, error) {
				allowed, err := filterFunctionPrev(mws)
				if !allowed || err != nil {
					return allowed, err
				}
				var ans float64
				// The level 3 CEL bot only filters by probable findability
				if botType != pb.BotRequest_LEVEL3_CEL_BOT {
					ans = botConfig.baseFindability * math.Pow(botConfig.parallelFindability, float64(len(mws)-1))
				} else {
					ans = 1.0
				}
				mw := mws[0] // assume len > 0
				if len(mw) >= game.ExchangeLimit {
					ans *= probableFindability(mw.String(), combinations(dist, subChooseCombos, mw.String(), true))
				}
				return r < ans, nil
			}
		}

		mws := []alphabet.MachineWord{}
		for _, play := range plays {
			var err error
			allowed := true
			if play.Action() == move.MoveTypePlay {
				mws, err = g.Board().FormedWords(play)
				if err != nil {
					log.Err(err).Msg("formed-words-filter-error")
					break
				}
				allowed, err = filterFunction(mws)
				if err != nil {
					log.Err(err).Msg("bot-type-move-filter-internal-error")
					break
				}
			}
			if allowed && err != nil {
				return play
			}
		}
		return passMove
	}
	if len(plays) > 0 {
		return plays[0]
	}
	return passMove
}

func probableFindability(word string, combos uint64) float64 {
	// This assumes the following preconditions:
	//   len(word) >= 2
	//   combos >= 1
	return math.Min(math.Log10(float64(combos))/float64(len(word)-1), 1.0)
}

func createSubCombos(dist *alphabet.LetterDistribution) [][]uint64 {
	// Adapted from GPL Zyzzyva's calculation code.
	maxFrequency := uint8(0)
	totalLetters := uint8(0)
	for _, value := range dist.Distribution {
		freq := value
		totalLetters += freq
		if freq > maxFrequency {
			maxFrequency = freq
		}
	}
	// Precalculate M choose N combinations
	r := uint8(1)
	subChooseCombos := make([][]uint64, maxFrequency+1)
	for i := uint8(0); i <= maxFrequency; i, r = i+1, r+1 {
		subList := make([]uint64, maxFrequency+1)
		for j := uint8(0); j <= maxFrequency; j++ {
			if (i == j) || (j == 0) {
				subList[j] = 1.0
			} else if i == 0 {
				subList[j] = 0.0
			} else {
				subList[j] = subChooseCombos[i-1][j-1] +
					subChooseCombos[i-1][j]
			}
		}
		subChooseCombos[i] = subList
	}
	return subChooseCombos
}

func combinations(dist *alphabet.LetterDistribution, subChooseCombos [][]uint64, alphagram string, withBlanks bool) uint64 {
	// Adapted from GPL Zyzzyva's calculation code.
	letters := make([]rune, 0)
	counts := make([]uint8, 0)
	combos := make([][]uint64, 0)
	for _, letter := range alphagram {
		foundLetter := false
		for j, char := range letters {
			if char == letter {
				counts[j]++
				foundLetter = true
				break
			}
		}
		if !foundLetter {
			letters = append(letters, letter)
			counts = append(counts, 1)
			combos = append(combos,
				subChooseCombos[dist.Distribution[letter]])

		}
	}
	totalCombos := uint64(0)
	numLetters := len(letters)
	// Calculate combinations with no blanks
	thisCombo := uint64(1)
	for i := 0; i < numLetters; i++ {
		thisCombo *= combos[i][counts[i]]
	}
	totalCombos += thisCombo
	if !withBlanks {
		return totalCombos
	}
	// Calculate combinations with one blank
	for i := 0; i < numLetters; i++ {
		counts[i]--
		thisCombo = subChooseCombos[dist.Distribution['?']][1]
		for j := 0; j < numLetters; j++ {
			thisCombo *= combos[j][counts[j]]
		}
		totalCombos += thisCombo
		counts[i]++
	}
	// Calculate combinations with two blanks
	for i := 0; i < numLetters; i++ {
		counts[i]--
		for j := i; j < numLetters; j++ {
			if counts[j] == 0 {
				continue
			}
			counts[j]--
			thisCombo = subChooseCombos[dist.Distribution['?']][2]

			for k := 0; k < numLetters; k++ {
				thisCombo *= combos[k][counts[k]]
			}
			totalCombos += thisCombo
			counts[j]++
		}
		counts[i]++
	}
	return totalCombos
}
