// Package gcgio implements a GCG parser. It might also implement
// other io methods.
package gcgio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	"github.com/domino14/macondo/mechanics"
	"github.com/rs/zerolog/log"
)

// A Token is an event in a GCG file.
type Token uint8

const (
	UndefinedToken Token = iota
	PlayerToken
	EncodingToken
	MoveToken
	NoteToken
	LexiconToken
	LostChallengeToken
	PassToken
	ChallengeBonusToken
	ExchangeToken
	EndRackPointsToken
	TimePenaltyToken
	LastRackPenaltyToken
)

type gcgdatum struct {
	token Token
	regex *regexp.Regexp
}

var GCGRegexes []gcgdatum

const (
	PlayerRegex             = `#player(?P<p_number>[1-2])\s+(?P<nick>\S+)\s+(?P<real_name>.+)`
	MoveRegex               = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+(?P<pos>\w+)\s+(?P<play>[\w\\.]+)\s+\+(?P<score>\d+)\s+(?P<cumul>\d+)`
	NoteRegex               = `#note (?P<note>.+)`
	LexiconRegex            = `#lexicon (?P<lexicon>.+)`
	CharacterEncodingRegex  = `#character-encoding (?P<encoding>.+)`
	LostChallengeRegex      = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+--\s+-(?P<lost_score>\d+)\s+(?P<cumul>\d+)`
	PassRegex               = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+-\s+\+0\s+(?P<cumul>\d+)`
	ChallengeBonusRegex     = `>(?P<nick>\S+):\s+(?P<rack>\S*)\s+\(challenge\)\s+\+(?P<bonus>\d+)\s+(?P<cumul>\d+)`
	ExchangeRegex           = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+-(?P<exchanged>\S+)\s+\+0\s+(?P<cumul>\d+)`
	EndRackPointsRegex      = `>(?P<nick>\S+):\s+\((?P<rack>\S+)\)\s+\+(?P<score>\d+)\s+(?P<cumul>\d+)`
	TimePenaltyRegex        = `>(?P<nick>\S+):\s+(?P<rack>\S*)\s+\(time\)\s+\-(?P<penalty>\d+)\s+(?P<cumul>\d+)`
	PtsLostForLastRackRegex = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+\((?P<rack>\S+)\)\s+\-(?P<penalty>\d+)\s+(?P<cumul>\d+)`
)

var compiledEncodingRegexp *regexp.Regexp

type parser struct {
	lastToken Token
}

// init initializes the regexp list.
func init() {
	// Important note: ChallengeBonusRegex is defined BEFORE EndRackPointsRegex.
	// That is because a line like  `>frentz:  (challenge) +5 534`  matches
	// both regexes. This can probably be avoided by being more strict about
	// what type of characters the rack can be, etc.

	compiledEncodingRegexp = regexp.MustCompile(CharacterEncodingRegex)

	GCGRegexes = []gcgdatum{
		gcgdatum{PlayerToken, regexp.MustCompile(PlayerRegex)},
		gcgdatum{EncodingToken, compiledEncodingRegexp},
		gcgdatum{MoveToken, regexp.MustCompile(MoveRegex)},
		gcgdatum{NoteToken, regexp.MustCompile(NoteRegex)},
		gcgdatum{LexiconToken, regexp.MustCompile(LexiconRegex)},
		gcgdatum{LostChallengeToken, regexp.MustCompile(LostChallengeRegex)},
		gcgdatum{PassToken, regexp.MustCompile(PassRegex)},
		gcgdatum{ChallengeBonusToken, regexp.MustCompile(ChallengeBonusRegex)},
		gcgdatum{ExchangeToken, regexp.MustCompile(ExchangeRegex)},
		gcgdatum{EndRackPointsToken, regexp.MustCompile(EndRackPointsRegex)},
		gcgdatum{TimePenaltyToken, regexp.MustCompile(TimePenaltyRegex)},
		gcgdatum{LastRackPenaltyToken, regexp.MustCompile(PtsLostForLastRackRegex)},
	}
}

func (p *parser) addEventOrPragma(token Token, match []string, gameRepr *mechanics.GameRepr) error {
	var err error

	switch token {
	case PlayerToken:
		pn, err := strconv.Atoi(match[1])
		if err != nil {
			return err
		}
		gameRepr.Players = append(gameRepr.Players, mechanics.PlayerInfo{
			Nickname:     match[2],
			RealName:     match[3],
			PlayerNumber: uint8(pn),
		})
		return nil
	case EncodingToken:
		return errors.New("encoding line must be first line in file if present")
	case MoveToken:
		evt := &mechanics.TilePlacementEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		evt.Position = match[3]
		evt.Play = match[4]
		evt.Score, err = strconv.Atoi(match[5])
		if err != nil {
			return err
		}
		evt.Cumulative, err = strconv.Atoi(match[6])
		if err != nil {
			return err
		}
		evt.CalculateCoordsFromPosition()
		evt.Type = mechanics.RegMove
		turn := []mechanics.Event{}
		turn = append(turn, evt)

		gameRepr.Turns = append(gameRepr.Turns, turn)

	case NoteToken:
		lastTurnIdx := len(gameRepr.Turns) - 1
		lastEvtIdx := len(gameRepr.Turns[lastTurnIdx]) - 1
		gameRepr.Turns[lastTurnIdx][lastEvtIdx].AppendNote(match[1])
		return nil
	case LexiconToken:
		gameRepr.Lexicon = match[1]
		return nil
	case LostChallengeToken:
		evt := &mechanics.ScoreSubtractionEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		score, err := strconv.Atoi(match[3])
		if err != nil {
			return err
		}
		evt.LostScore = score
		evt.Cumulative, err = strconv.Atoi(match[4])
		if err != nil {
			return err
		}
		evt.Type = mechanics.LostChallenge
		// This can not be a stand-alone turn; it must be added to the last
		// turn.
		lastTurnIdx := len(gameRepr.Turns) - 1
		gameRepr.Turns[lastTurnIdx] = append(gameRepr.Turns[lastTurnIdx], evt)
	case TimePenaltyToken:
		evt := &mechanics.ScoreSubtractionEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		score, err := strconv.Atoi(match[3])
		if err != nil {
			return err
		}
		evt.LostScore = score
		evt.Cumulative, err = strconv.Atoi(match[4])
		if err != nil {
			return err
		}
		evt.Type = mechanics.TimePenalty
		lastTurnIdx := len(gameRepr.Turns) - 1
		gameRepr.Turns[lastTurnIdx] = append(gameRepr.Turns[lastTurnIdx], evt)

	case LastRackPenaltyToken:
		evt := &mechanics.ScoreSubtractionEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		if evt.Rack != match[3] {
			return fmt.Errorf("last rack penalty event malformed")
		}
		score, err := strconv.Atoi(match[4])
		if err != nil {
			return err
		}
		evt.LostScore = score
		evt.Cumulative, err = strconv.Atoi(match[5])
		if err != nil {
			return err
		}
		evt.Type = mechanics.EndRackPenalty
		lastTurnIdx := len(gameRepr.Turns) - 1
		gameRepr.Turns[lastTurnIdx] = append(gameRepr.Turns[lastTurnIdx], evt)

	case PassToken:
		evt := &mechanics.PassingEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		evt.Cumulative, err = strconv.Atoi(match[3])
		if err != nil {
			return err
		}
		evt.Type = mechanics.Pass
		turn := []mechanics.Event{evt}
		gameRepr.Turns = append(gameRepr.Turns, turn)

	case ChallengeBonusToken, EndRackPointsToken:
		evt := &mechanics.ScoreAdditionEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		if token == ChallengeBonusToken {
			evt.Bonus, err = strconv.Atoi(match[3])
		} else {
			evt.EndRackPoints, err = strconv.Atoi(match[3])
		}
		if err != nil {
			return err
		}
		evt.Cumulative, err = strconv.Atoi(match[4])
		if err != nil {
			return err
		}
		if token == ChallengeBonusToken {
			evt.Type = mechanics.ChallengeBonus
		} else if token == EndRackPointsToken {
			evt.Type = mechanics.EndRackPts
		}
		lastTurnIdx := len(gameRepr.Turns) - 1
		gameRepr.Turns[lastTurnIdx] = append(gameRepr.Turns[lastTurnIdx], evt)

	case ExchangeToken:
		evt := &mechanics.PassingEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		evt.Exchanged = match[3]
		evt.Cumulative, err = strconv.Atoi(match[4])
		if err != nil {
			return err
		}
		evt.Type = mechanics.Exchange
		turn := []mechanics.Event{evt}
		gameRepr.Turns = append(gameRepr.Turns, turn)

	}
	return nil
}

func (p *parser) parseLine(line string, gameRepr *mechanics.GameRepr) error {

	foundMatch := false

	for _, datum := range GCGRegexes {
		match := datum.regex.FindStringSubmatch(line)
		if match != nil {
			foundMatch = true
			err := p.addEventOrPragma(datum.token, match, gameRepr)
			if err != nil {
				return err
			}
			p.lastToken = datum.token
			break
		}
	}
	if !foundMatch {
		log.Debug().Msgf("Found no match for line '%v'", line)

		// maybe it's a multi-line note.
		if p.lastToken == NoteToken {
			lastTurnIdx := len(gameRepr.Turns) - 1
			lastEventIdx := len(gameRepr.Turns[lastTurnIdx]) - 1
			gameRepr.Turns[lastTurnIdx][lastEventIdx].AppendNote("\n" + line)
			return nil
		}
		// ignore empty lines
		if strings.TrimSpace(line) == "" {
			return nil
		}
		return fmt.Errorf("no match found for line '%v'", line)
	}
	return nil
}

func encodingOrFirstLine(reader io.Reader) (string, string, error) {
	// Read either the encoding of the file, or the first line,
	// whichever is available.
	const BufSize = 128
	buf := make([]byte, BufSize)
	n := 0
	for {
		// non buffered byte-by-byte
		if _, err := reader.Read(buf[n : n+1]); err != nil {
			return "", "", err
		}
		if buf[n] == 0xa || n == BufSize { // reached CR or size limit
			firstLine := buf[:n]
			match := compiledEncodingRegexp.FindStringSubmatch(string(firstLine))
			if match != nil {
				enc := strings.ToLower(match[1])
				if enc != "utf-8" && enc != "utf8" {
					return "", "", errors.New("unhandled character encoding " + enc)
				}
				// Otherwise, switch to utf8 mode; which means we require no transform
				// since Go does UTF-8 by default.
				return "utf8", "", nil
			}
			// Not an encoding line. We should ocnvert the raw bytes into the default
			// GCG encoding, which is ISO 8859-1.
			decoder := charmap.ISO8859_1.NewDecoder()
			result, _, err := transform.Bytes(decoder, firstLine)
			if err != nil {
				return "", "", err
			}
			// We can stringify the result now, as the transformed bytes will
			// be UTF-8
			return "", string(result), nil
		}
		n++

	}
}

func ParseGCGFromReader(reader io.Reader) (*mechanics.GameRepr, error) {

	grep := &mechanics.GameRepr{Turns: []mechanics.Turn{}, Players: []mechanics.PlayerInfo{},
		Version: 1}
	var err error
	parser := &parser{}
	originalGCG := ""

	// Determine encoding from first line
	// Try to match to an encoding pragma line. If it doesn't exist,
	// the encoding is ISO 8859-1 per spec.
	enc, firstLine, err := encodingOrFirstLine(reader)
	if err != nil {
		return nil, err
	}
	var scanner *bufio.Scanner
	if enc != "utf8" {
		gcgEncoding := charmap.ISO8859_1
		r := transform.NewReader(reader, gcgEncoding.NewDecoder())
		scanner = bufio.NewScanner(r)
	} else {
		scanner = bufio.NewScanner(reader)
	}
	if firstLine != "" {
		err = parser.parseLine(firstLine, grep)
		if err != nil {
			return nil, err
		}
		originalGCG += firstLine + "\n"
	}

	for scanner.Scan() {
		line := scanner.Text()
		err = parser.parseLine(line, grep)
		if err != nil {
			return nil, err
		}
		originalGCG += line + "\n"
	}
	grep.OriginalGCG = strings.TrimSpace(originalGCG)
	return grep, nil
}

// ParseGCG parses a GCG file into a GameRepr.
func ParseGCG(filename string) (*mechanics.GameRepr, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	return ParseGCGFromReader(f)
}

func writeGCGHeader(s *strings.Builder) {
	s.WriteString("#character-encoding UTF-8\n")
	log.Debug().Msg("wrote encoding")
}

func writePlayer(s *strings.Builder, p mechanics.PlayerInfo) {
	fmt.Fprintf(s, "#player%d %v %v\n", p.PlayerNumber, p.Nickname, p.RealName)
}

func writeEvent(s *strings.Builder, evt mechanics.Event) {

	nick := evt.GetNickname()
	rack := evt.GetRack()
	evtType := evt.GetType()

	// XXX HANDLE MORE TYPES (e.g. time penalty at some point, end rack
	// penalty for international rules)
	switch evtType {
	case mechanics.RegMove:
		// sevt = specific event
		sevt := evt.(*mechanics.TilePlacementEvent)
		fmt.Fprintf(s, ">%v: %v %v %v +%d %d\n",
			nick, rack, sevt.Position, sevt.Play, sevt.Score, sevt.Cumulative,
		)
	case mechanics.LostChallenge:
		sevt := evt.(*mechanics.ScoreSubtractionEvent)
		// >emely: DEIILTZ -- -24 55

		fmt.Fprintf(s, ">%v: %v -- -%d %d\n",
			nick, rack, sevt.LostScore, sevt.Cumulative)

	case mechanics.Pass:
		// >Randy: U - +0 380
		sevt := evt.(*mechanics.PassingEvent)

		fmt.Fprintf(s, ">%v: (%v) - +0 %d\n", nick, rack, sevt.Cumulative)
	case mechanics.ChallengeBonus:
		// >Joel: DROWNUG (challenge) +5 289
		sevt := evt.(*mechanics.ScoreAdditionEvent)
		fmt.Fprintf(s, ">%v: %v (challenge) +%d %d\n",
			nick, rack, sevt.Bonus, sevt.Cumulative)

	case mechanics.EndRackPts:
		// >Dave: (G) +4 539
		sevt := evt.(*mechanics.ScoreAdditionEvent)
		fmt.Fprintf(s, ">%v: (%v) +%d %d\n",
			nick, rack, sevt.EndRackPoints, sevt.Cumulative)

	case mechanics.Exchange:
		// >Marlon: SEQSPO? -QO +0 268
		sevt := evt.(*mechanics.PassingEvent)
		fmt.Fprintf(s, ">%v: %v -%v +0 %d\n",
			nick, rack, sevt.Exchanged, sevt.Cumulative)

	}

}

func writeTurn(s *strings.Builder, t mechanics.Turn) {
	for _, evt := range t {
		writeEvent(s, evt)
	}
}

// ToGCG returns a string GCG representation of the GameRepr.
func GameReprToGCG(r *mechanics.GameRepr) string {

	var str strings.Builder
	writeGCGHeader(&str)
	for _, player := range r.Players {
		writePlayer(&str, player)
	}

	for _, turn := range r.Turns {
		writeTurn(&str, turn)
	}

	return str.String()
}