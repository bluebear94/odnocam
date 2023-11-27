package negamax

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
	"lukechampine.com/frand"

	"github.com/domino14/macondo/game"
	pb "github.com/domino14/macondo/gen/api/proto/macondo"
	"github.com/domino14/macondo/move"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/macondo/tinymove"
	"github.com/domino14/macondo/tinymove/conversions"
)

// thanks Wikipedia:
/*
function negamax(node, depth, α, β, color) is
    if depth = 0 or node is a terminal node then
        return color × the heuristic value of node

    childNodes := generateMoves(node)
    childNodes := orderMoves(childNodes)
    value := −∞
    foreach child in childNodes do
        value := max(value, −negamax(child, depth − 1, −β, −α, −color))
        α := max(α, value)
        if α ≥ β then
            break (* cut-off *)
    return value
(* Initial call for Player A's root node *)
negamax(rootNode, depth, −∞, +∞, 1)
**/

const HugeNumber = int16(32767)
const MaxVariantLength = 25
const EarlyPassOffset = 21000
const HashMoveOffset = 6000
const MaxLazySMPThreads = 6

var (
	ErrNoEndgameSolution = errors.New("no endgame solution found")
)

// Credit: MIT-licensed https://github.com/algerbrex/blunder/blob/main/engine/search.go
type PVLine struct {
	Moves    [MaxVariantLength]*move.Move
	g        *game.Game
	score    int16
	numMoves int
}

// Clear the principal variation line.
func (pvLine *PVLine) Clear() {
	pvLine.numMoves = 0
}

// Update the principal variation line with a new best move,
// and a new line of best play after the best move.
func (pvLine *PVLine) Update(m *move.Move, newPVLine PVLine, score int16) {
	pvLine.Clear()
	mc := &move.Move{}
	mc.CopyFrom(m)
	pvLine.Moves[0] = mc
	for i := 0; i < newPVLine.numMoves; i++ {
		pvLine.Moves[i+1] = newPVLine.Moves[i]
	}
	pvLine.numMoves = newPVLine.numMoves + 1
	pvLine.score = score
}

// Get the best move from the principal variation line.
func (pvLine *PVLine) GetPVMove() *move.Move {
	return pvLine.Moves[0]
}

// Convert the principal variation line to a string.
func (pvLine PVLine) String() string {
	var s string
	s = fmt.Sprintf("PV; val %d\n", pvLine.score)
	for i := 0; i < pvLine.numMoves; i++ {
		s += fmt.Sprintf("%d: %s (%d)\n",
			i+1,
			pvLine.Moves[i].ShortDescription(),
			pvLine.Moves[i].Score())
	}
	return s
}

func (pvLine PVLine) NLBString() string {
	// no line breaks
	var s string
	s = fmt.Sprintf("PV; val %d; ", pvLine.score)
	for i := 0; i < pvLine.numMoves; i++ {
		s += fmt.Sprintf("%d: %s (%d); ",
			i+1,
			pvLine.Moves[i].ShortDescription(),
			pvLine.Moves[i].Score())
	}
	return s
}

// panic if pvline is invalid
// func (p PVLine) verify() {
// 	g := p.g.Copy()
// 	onturn := g.PlayerOnTurn()
// 	initSpread := g.SpreadFor(onturn)
// 	for i := 0; i < len(p.Moves); i++ {
// 		mc := &move.Move{}
// 		p.Moves[i].CopyToMove(mc)
// 		_, err := g.ValidateMove(mc)
// 		if err != nil {
// 			fmt.Println("error with pv", p)
// 			panic(err)
// 		}
// 		err = g.PlayMove(mc, false, 0)
// 		if err != nil {
// 			panic(err)
// 		}
// 	}
// 	// If the scores don't match, log a warning. This can be because
// 	// the transposition table cut off the PV.
// 	if g.SpreadFor(onturn)-initSpread != int(p.score) {
// 		log.Warn().
// 			Int("initSpread", initSpread).
// 			Int("nowSpread", g.SpreadFor(onturn)).
// 			Int("diffInSpreads", g.SpreadFor(onturn)-initSpread).
// 			Int16("expectedDiff", p.score).
// 			Msg("pv-cutoff-spreads-do-not-match")
// 	}
// }

type Solver struct {
	stmMovegen   movegen.MoveGenerator
	game         *game.Game
	initialMoves [][]tinymove.SmallMove

	gameCopies []*game.Game
	movegens   []movegen.MoveGenerator

	initialSpread int
	solvingPlayer int // This is the player who we call this function for.

	// earlyPassOptim: if the last move was a pass, try a pass first to end
	// the game. It costs very little to check this case first and results
	// in a modest speed boost.
	earlyPassOptim          bool
	iterativeDeepeningOptim bool
	firstWinOptim           bool
	transpositionTableOptim bool
	lazySMPOptim            bool
	principalVariation      PVLine
	bestPVValue             int16

	ttable *TranspositionTable

	currentIDDepths []int
	requestedPlies  int
	threads         int
	nodes           atomic.Uint64

	logStream io.Writer
	busy      bool
}

// Init initializes the solver
func (s *Solver) Init(m movegen.MoveGenerator, game *game.Game) error {
	s.ttable = GlobalTranspositionTable
	s.stmMovegen = m
	s.game = game
	s.earlyPassOptim = true

	s.firstWinOptim = false
	s.transpositionTableOptim = true
	s.iterativeDeepeningOptim = true
	s.threads = max(1, runtime.NumCPU())
	if s.stmMovegen != nil {
		s.stmMovegen.SetGenPass(true)
		s.stmMovegen.SetPlayRecorder(movegen.AllPlaysSmallRecorder)
	}

	return nil
}

func (s *Solver) SetThreads(threads int) {
	switch {
	case threads < 2:
		s.threads = 1
		s.lazySMPOptim = false
	case threads >= 2:
		s.threads = threads
		s.lazySMPOptim = true
	}
}

func (s *Solver) makeGameCopies() error {
	log.Debug().Int("threads", s.threads).Msg("makeGameCopies")
	s.gameCopies = []*game.Game{}
	for i := 0; i < s.threads-1; i++ {
		s.gameCopies = append(s.gameCopies, s.game.Copy())
		s.gameCopies[i].SetBackupMode(game.SimulationMode)
	}
	s.movegens = []movegen.MoveGenerator{}
	gaddag := s.stmMovegen.(*movegen.GordonGenerator).GADDAG()
	for i := 0; i < s.threads-1; i++ {
		mg := movegen.NewGordonGenerator(gaddag, s.gameCopies[i].Board(), s.gameCopies[i].Bag().LetterDistribution())
		mg.SetSortingParameter(movegen.SortByNone)
		mg.SetGenPass(true)
		mg.SetPlayRecorder(movegen.AllPlaysSmallRecorder)
		s.movegens = append(s.movegens, mg)
	}
	return nil
}

func (s *Solver) generateSTMPlays(depth, thread int) []tinymove.SmallMove {
	// STM means side-to-move
	g := s.game
	mg := s.stmMovegen

	if thread > 0 {
		mg = s.movegens[thread-1]
		g = s.gameCopies[thread-1]
	}
	var genPlays []tinymove.SmallMove

	stmRack := g.RackFor(g.PlayerOnTurn())
	if s.currentIDDepths[thread] == depth {
		genPlays = s.initialMoves[thread]
	} else {
		mg.GenAll(stmRack, false)
		plays := mg.SmallPlays()
		// movegen owns the plays array. Make a copy of these.
		genPlays = make([]tinymove.SmallMove, len(plays))
		copy(genPlays, plays)
		movegen.SmallPlaySlicePool.Put(&plays)
	}
	return genPlays
}

func (s *Solver) assignEstimates(moves []tinymove.SmallMove, depth, thread int, ttMove tinymove.TinyMove) {
	g := s.game

	if thread > 0 {
		g = s.gameCopies[thread-1]
	}

	stmRack := g.RackFor(g.PlayerOnTurn())
	pnot := (g.PlayerOnTurn() + 1) % g.NumPlayers()
	otherRack := g.RackFor(pnot)
	numTilesOnRack := int(stmRack.NumTiles())
	ld := g.Bag().LetterDistribution()

	lastMoveWasPass := g.ScorelessTurns() > g.LastScorelessTurns()
	for idx := range moves {
		if moves[idx].TilesPlayed() == numTilesOnRack {
			moves[idx].SetEstimatedValue(int16(moves[idx].Score() + 2*otherRack.ScoreOn(ld)))
			// } else if thread == 4 {
			// 	// Some handwavy LazySMP thing.
			// 	p.SetEstimatedValue(int16(7 - p.TilesPlayed()))
		} else if depth > 2 {
			moves[idx].SetEstimatedValue(int16(moves[idx].Score() + 3*moves[idx].TilesPlayed()))
		} else {
			moves[idx].SetEstimatedValue(int16(moves[idx].Score()))
		}
		if moves[idx].TinyMove() == ttMove {
			moves[idx].AddEstimatedValue(HashMoveOffset)
		}

		// XXX: should also verify validity of ttMove later.
		if s.earlyPassOptim && lastMoveWasPass && moves[idx].IsPass() {
			moves[idx].AddEstimatedValue(EarlyPassOffset)
		}
	}
	sort.Slice(moves, func(i int, j int) bool {
		return moves[i].EstimatedValue() > moves[j].EstimatedValue()
	})
}

func (s *Solver) iterativelyDeepenLazySMP(ctx context.Context, plies int) error {
	// Generate first layer of moves.
	if plies < 2 {
		return errors.New("use at least 2 plies")
	}
	s.makeGameCopies()
	log.Info().Int("threads", s.threads).Msg("using-lazy-smp")
	s.currentIDDepths = make([]int, s.threads)
	initialHashKey := uint64(0)
	if s.transpositionTableOptim {
		initialHashKey = s.ttable.Zobrist().Hash(
			s.game.Board().GetSquares(),
			s.game.RackFor(s.solvingPlayer),
			s.game.RackFor(1-s.solvingPlayer),
			false, s.game.ScorelessTurns(),
		)
	}

	α := -HugeNumber
	β := HugeNumber
	if s.firstWinOptim {
		// Search a very small window centered around 0. We're just trying
		// to find something that surpasses it.
		α = -1
		β = 1
	}

	// Generate first layer of moves.
	s.currentIDDepths[0] = -1 // so that generateSTMPlays generates all moves first properly.
	s.initialMoves = make([][]tinymove.SmallMove, s.threads)
	s.initialMoves[0] = s.generateSTMPlays(0, 0)

	// assignEstimates for the very first time around.
	s.assignEstimates(s.initialMoves[0], 0, 0, tinymove.InvalidTinyMove)

	pv := PVLine{g: s.game}
	// Do initial search so that we can have a good estimate for
	// move ordering.
	s.currentIDDepths[0] = 1
	s.negamax(ctx, initialHashKey, 1, α, β, &pv, 0)
	// Sort the moves by valuation.
	sort.Slice(s.initialMoves[0], func(i, j int) bool {
		return s.initialMoves[0][i].EstimatedValue() > s.initialMoves[0][j].EstimatedValue()
	})

	// copy these moves to per-thread subarrays. This will also copy
	// the initial estimated valuation and order.
	for t := 1; t < s.threads; t++ {
		s.initialMoves[t] = make([]tinymove.SmallMove, len(s.initialMoves[0]))
		copy(s.initialMoves[t], s.initialMoves[0])
	}

	for p := 2; p <= plies; p++ {
		log.Info().Int("plies", p).Msg("deepening-iteratively")
		s.currentIDDepths[0] = p
		if s.logStream != nil {
			fmt.Fprintf(s.logStream, "- ply: %d\n", p)
		}

		// start helper threads
		g := errgroup.Group{}
		cancels := make([]context.CancelFunc, s.threads-1)
		for t := 1; t < s.threads; t++ {
			t := t
			p := p
			// search to different plies for different threads
			s.currentIDDepths[t] = p + t%3
			helperCtx, cancel := context.WithCancel(ctx)
			cancels[t-1] = cancel
			helperAlpha := -HugeNumber
			helperBeta := HugeNumber
			if s.firstWinOptim {
				helperAlpha = -1
				helperBeta = 1
			}
			g.Go(func() error {
				defer func() {
					log.Debug().Msgf("Thread %d exiting", t)
				}()
				log.Debug().Msgf("Thread %d starting; searching %d deep", t, p+t%2)

				// ignore the score for these helper threads; we're just
				// using them to help build up the transposition table.
				pv := PVLine{g: s.gameCopies[t-1]} // not being used for anything
				val, err := s.negamax(
					helperCtx, initialHashKey, s.currentIDDepths[t],
					helperAlpha, helperBeta, &pv, t)
				if err != nil {
					log.Debug().Msgf("Thread %d error %v", t, err)
				}
				log.Debug().Msgf("Thread %d done; val returned %d, pv %s", t, val, pv.NLBString())
				// Try a few schemes to really randomize stuff.
				if t == 1 {
					sort.Slice(s.initialMoves[t], func(i, j int) bool {
						return s.initialMoves[t][i].EstimatedValue() > s.initialMoves[t][j].EstimatedValue()
					})
				} else if t == 2 {
					// do nothing, use original order
				} else if t > 2 && t <= 7 {
					// Shuffle the order of root nodes
					frand.Shuffle(len(s.initialMoves[t]), func(i, j int) {
						s.initialMoves[t][i], s.initialMoves[t][j] = s.initialMoves[t][j], s.initialMoves[t][i]
					})
				} else if t > 7 {
					// A sort of restricted shuffle?
					topfew := len(s.initialMoves[t]) / 3

					frand.Shuffle(topfew, func(i, j int) {
						s.initialMoves[t][i], s.initialMoves[t][j] = s.initialMoves[t][j], s.initialMoves[t][i]
					})
					frand.Shuffle(len(s.initialMoves)-topfew, func(i, j int) {
						s.initialMoves[t][i+topfew], s.initialMoves[t][j+topfew] = s.initialMoves[t][j+topfew], s.initialMoves[t][i+topfew]
					})
				}
				return err
			})
		}

		// This is the main thread. All other threads just help update the
		// transposition table, but this one actually edits the principal
		// variation.
		pv := PVLine{g: s.game}
		val, err := s.negamax(ctx, initialHashKey, p, α, β, &pv, 0)

		if err != nil {
			log.Err(err).Msg("negamax-error-most-likely-timeout")
		} else {
			sort.Slice(s.initialMoves[0], func(i, j int) bool {
				return s.initialMoves[0][i].EstimatedValue() > s.initialMoves[0][j].EstimatedValue()
			})

			s.principalVariation = pv
			s.bestPVValue = val - int16(s.initialSpread)
			log.Info().Int16("spread", val).Int("ply", p).Str("pv", pv.NLBString()).Msg("best-val")
		}
		// stop helper threads cleanly
		for _, c := range cancels {
			if c != nil {
				c()
			}
		}

		err = g.Wait()
		if err != nil {
			if err.Error() == "context canceled" {
				log.Debug().Msg("helper threads exited with a canceled context")
			} else {
				return err
			}
		}

	}
	return nil
}

// iterativelyDeepen is single-threaded version.
func (s *Solver) iterativelyDeepen(ctx context.Context, plies int) error {
	if s.lazySMPOptim {
		return s.iterativelyDeepenLazySMP(ctx, plies)
	}
	s.currentIDDepths = make([]int, 1)
	g := s.game

	initialHashKey := uint64(0)
	if s.transpositionTableOptim {
		initialHashKey = s.ttable.Zobrist().Hash(
			g.Board().GetSquares(),
			g.RackFor(s.solvingPlayer),
			g.RackFor(1-s.solvingPlayer),
			false, g.ScorelessTurns(),
		)
	}

	α := -HugeNumber
	β := HugeNumber
	if s.firstWinOptim {
		// Search a very small window centered around 0. We're just trying
		// to find something that surpasses it.
		α = -1
		β = 1
	}

	// Generate first layer of moves.
	s.currentIDDepths[0] = -1 // so that generateSTMPlays generates all moves first properly.
	s.initialMoves = make([][]tinymove.SmallMove, 1)
	s.initialMoves[0] = s.generateSTMPlays(0, 0)
	// assignEstimates for the very first time around.
	s.assignEstimates(s.initialMoves[0], 0, 0, tinymove.InvalidTinyMove)
	start := 1
	if !s.iterativeDeepeningOptim {
		start = plies
	}

	for p := start; p <= plies; p++ {
		log.Info().Int("plies", p).Msg("deepening-iteratively")
		s.currentIDDepths[0] = p
		if s.logStream != nil {
			fmt.Fprintf(s.logStream, "- ply: %d\n", p)
		}
		pv := PVLine{g: g}
		val, err := s.negamax(ctx, initialHashKey, p, α, β, &pv, 0)
		if err != nil {
			return err
		}
		log.Info().Int16("spread", val).Int("ply", p).Str("pv", pv.NLBString()).Msg("best-val")
		// Sort top layer of moves by value for the next time around.
		sort.Slice(s.initialMoves[0], func(i, j int) bool {
			return s.initialMoves[0][i].EstimatedValue() > s.initialMoves[0][j].EstimatedValue()
		})
		s.principalVariation = pv
		s.bestPVValue = val - int16(s.initialSpread)
	}
	return nil

}

func (s *Solver) negamax(ctx context.Context, nodeKey uint64, depth int, α, β int16, pv *PVLine, thread int) (int16, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	g := s.game
	if thread > 0 {
		g = s.gameCopies[thread-1]
	}
	onTurn := g.PlayerOnTurn()
	ourSpread := g.SpreadFor(onTurn)

	// Note: if I return early as in here, the PV might not be complete.
	// (the transposition table is cutting off the iterations)
	// The value should still be correct, though.
	// Something like PVS might do better at keeping the PV intact.
	alphaOrig := α
	ttMove := tinymove.InvalidTinyMove

	if s.transpositionTableOptim {
		ttEntry := s.ttable.lookup(nodeKey)
		if ttEntry.valid() && ttEntry.depth() >= uint8(depth) {
			score := ttEntry.score
			flag := ttEntry.flag()
			// add spread back in; we subtract them when storing.
			score += int16(ourSpread)
			if flag == TTExact {
				if pv.numMoves == 0 {
					// let's not lose the very first move.
					log.Trace().Msg("exact-tt-move")
					childPV := PVLine{g: g}
					child, err := conversions.TinyMoveToFullMove(ttEntry.move(), g.Board(),
						g.Bag().LetterDistribution(), g.RackFor(onTurn))
					if err != nil {
						return 0, err
					}
					pv.Update(child, childPV, score)
				}
				return score, nil
			} else if flag == TTLower {
				α = max(α, score)
			} else if flag == TTUpper {
				β = min(β, score)
			}
			if α >= β {
				if pv.numMoves == 0 {
					// let's not lose the very first move.
					log.Trace().Msg("alpha-beta-cutoff-at-tt")
					childPV := PVLine{g: g}
					child, err := conversions.TinyMoveToFullMove(ttEntry.move(), g.Board(),
						g.Bag().LetterDistribution(), g.RackFor(onTurn))
					if err != nil {
						return 0, err
					}
					pv.Update(child, childPV, score)
				}
				return score, nil
			}
			// search hash move first.
			ttMove = ttEntry.move()
		}
	}

	if depth == 0 || g.Playing() != pb.PlayState_PLAYING {
		// Evaluate the state.
		// A very simple evaluation function for now. Just the current spread,
		// even if the game is not over yet.
		spreadNow := g.SpreadFor(g.PlayerOnTurn())
		return int16(spreadNow), nil
	}
	childPV := PVLine{g: g}

	children := s.generateSTMPlays(depth, thread)
	stmRack := g.RackFor(g.PlayerOnTurn())
	if s.currentIDDepths[thread] != depth {
		// If we're not at the top level, assign estimates. Otherwise,
		// we assign the values as estimates in the loop below.
		s.assignEstimates(children, depth, thread, ttMove)
	}

	bestValue := -HugeNumber
	indent := 2 * (s.currentIDDepths[thread] - depth)
	if s.logStream != nil {
		fmt.Fprintf(s.logStream, "  %vplays:\n", strings.Repeat(" ", indent))
	}
	var bestMove tinymove.SmallMove
	for idx := range children {
		if s.logStream != nil {
			fmt.Fprintf(s.logStream, "  %v- play: %v\n", strings.Repeat(" ", indent), children[idx].ShortDescription(g.Alphabet()))
		}

		moveTiles, err := g.PlaySmallMove(&children[idx])
		if err != nil {
			return 0, err
		}
		s.nodes.Add(1)
		childKey := uint64(0)
		if s.transpositionTableOptim {
			childKey = s.ttable.Zobrist().AddMove(nodeKey, &children[idx], stmRack, moveTiles,
				onTurn == s.solvingPlayer, g.ScorelessTurns(), g.LastScorelessTurns())
		}
		value, err := s.negamax(ctx, childKey, depth-1, -β, -α, &childPV, thread)
		if err != nil {
			g.UnplayLastMove()
			return value, err
		}
		g.UnplayLastMove()
		if s.logStream != nil {
			fmt.Fprintf(s.logStream, "  %v  value: %v\n", strings.Repeat(" ", indent), value)
		}
		if -value > bestValue {
			bestValue = -value
			bestMove = children[idx]
			// allocate a move to update the pv
			m := &move.Move{}
			conversions.SmallMoveToMove(&bestMove, m, g.Alphabet(), g.Board(), stmRack)
			pv.Update(m, childPV, bestValue-int16(s.initialSpread))
		}
		if s.currentIDDepths[thread] == depth {
			children[idx].SetEstimatedValue(-value)
		}

		α = max(α, bestValue)
		if s.logStream != nil {
			fmt.Fprintf(s.logStream, "  %v  α: %v\n", strings.Repeat(" ", indent), α)
			fmt.Fprintf(s.logStream, "  %v  β: %v\n", strings.Repeat(" ", indent), β)
		}
		if bestValue >= β {
			break // beta cut-off
		}
		childPV.Clear() // clear the child node's pv for the next child node
	}
	if s.transpositionTableOptim {
		// We store this value without our spread to make it spread-independent.
		// Without this, we need to hash the spread as well and this results
		// in many more TT misses.

		score := bestValue - int16(ourSpread)
		var flag uint8
		entryToStore := TableEntry{
			score: score,
		}
		if bestValue <= alphaOrig {
			flag = TTUpper
		} else if bestValue >= β {
			flag = TTLower
		} else {
			flag = TTExact
		}
		entryToStore.flagAndDepth = flag<<6 + uint8(depth)
		entryToStore.play = bestMove.TinyMove()
		s.ttable.store(nodeKey, entryToStore)
	}
	return bestValue, nil

}

func (s *Solver) Solve(ctx context.Context, plies int) (int16, []*move.Move, error) {
	s.busy = true
	defer func() {
		s.busy = false
	}()
	s.solvingPlayer = s.game.PlayerOnTurn()
	// Make sure the other player's rack isn't empty.
	if s.game.RackFor(1-s.solvingPlayer).NumTiles() == 0 {
		_, err := s.game.SetRandomRack(1-s.solvingPlayer, nil)
		if err != nil {
			return 0, nil, err
		}
	}
	if s.game.Bag().TilesRemaining() > 0 {
		return 0, nil, errors.New("bag is not empty; cannot use endgame solver")
	}
	log.Debug().Int("plies", plies).Msg("alphabeta-solve-config")
	s.requestedPlies = plies
	tstart := time.Now()
	s.stmMovegen.SetSortingParameter(movegen.SortByNone)
	defer s.stmMovegen.SetSortingParameter(movegen.SortByScore)
	if s.lazySMPOptim {
		if s.transpositionTableOptim {
			s.ttable.SetMultiThreadedMode()
		} else {
			return 0, nil, errors.New("cannot use lazySMP optimization without transposition table")
		}
	} else {
		s.ttable.SetSingleThreadedMode()
	}
	if s.transpositionTableOptim {
		s.ttable.Reset(s.game.Config().TTableFractionOfMem, s.game.Board().Dim())
	}
	s.game.SetEndgameMode(true)
	defer s.game.SetEndgameMode(false)

	s.initialSpread = s.game.CurrentSpread()
	log.Debug().Msgf("Player %v spread at beginning of endgame: %v (%d)", s.solvingPlayer, s.initialSpread, s.game.ScorelessTurns())
	s.nodes.Store(0)
	var bestV int16
	var bestSeq []*move.Move
	// + 2 since lazysmp can search at a higher ply count
	s.game.SetStateStackLength(plies + 2)

	g := &errgroup.Group{}
	done := make(chan bool)

	g.Go(func() error {
		ticker := time.NewTicker(1 * time.Second)
		var lastNodes uint64
		for {
			select {
			case <-done:
				return nil
			case <-ticker.C:
				nodes := s.nodes.Load()
				log.Debug().Uint64("nps", nodes-lastNodes).Msg("nodes-per-second")
				lastNodes = nodes
			}
		}

	})

	g.Go(func() error {
		if s.lazySMPOptim && !s.iterativeDeepeningOptim {
			return errors.New("cannot use lazySMP if iterative deepening is off")
		}
		log.Debug().Msgf("Using iterative deepening with %v max plies", plies)
		err := s.iterativelyDeepen(ctx, plies)
		done <- true
		return err
	})

	err := g.Wait()
	// Go down tree and find best variation:

	bestSeq = s.principalVariation.Moves[:s.principalVariation.numMoves]
	bestV = s.bestPVValue
	log.Info().
		Uint64("ttable-created", s.ttable.created.Load()).
		Uint64("ttable-lookups", s.ttable.lookups.Load()).
		Uint64("ttable-hits", s.ttable.hits.Load()).
		Uint64("ttable-t2collisions", s.ttable.t2collisions.Load()).
		Float64("time-elapsed-sec", time.Since(tstart).Seconds()).
		Msg("solve-returning")
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			// ignore
			err = nil
		}
	}
	return bestV, bestSeq, err
}

// QuickAndDirtySolve is meant for a pre-endgame engine to call this function
// without having to initialize everything. The caller is responsible for
// initializations of data structures. It is single-threaded as well.
func (s *Solver) QuickAndDirtySolve(ctx context.Context, plies, thread int) (int16, []*move.Move, error) {
	s.busy = true
	defer func() {
		s.busy = false
	}()
	// Make sure the other player's rack isn't empty.
	s.solvingPlayer = s.game.PlayerOnTurn()
	if s.game.RackFor(1-s.solvingPlayer).NumTiles() == 0 {
		_, err := s.game.SetRandomRack(1-s.solvingPlayer, nil)
		if err != nil {
			return 0, nil, err
		}
	}
	if s.game.Bag().TilesRemaining() > 0 {
		return 0, nil, errors.New("bag is not empty; cannot use endgame solver")
	}
	log.Debug().
		Int("thread", thread).
		Str("ourRack", s.game.RackLettersFor(s.solvingPlayer)).
		Str("theirRack", s.game.RackLettersFor(1-s.solvingPlayer)).
		Int("plies", plies).Msg("qdsolve-alphabeta-solve-config")
	s.requestedPlies = plies
	tstart := time.Now()
	s.stmMovegen.SetSortingParameter(movegen.SortByNone)
	defer s.stmMovegen.SetSortingParameter(movegen.SortByScore)

	s.initialSpread = s.game.CurrentSpread()
	log.Debug().Int("thread", thread).Msgf("Player %v spread at beginning of endgame: %v (%d)", s.solvingPlayer, s.initialSpread, s.game.ScorelessTurns())

	var bestV int16
	var bestSeq []*move.Move

	// err := s.iterativelyDeepen(ctx, plies)
	// if err != nil {
	// 	log.Debug().AnErr("err", err).Msg("error iteratively deepening")
	// }
	initialHashKey := uint64(0)
	if s.transpositionTableOptim {
		initialHashKey = s.ttable.Zobrist().Hash(
			s.game.Board().GetSquares(),
			s.game.RackFor(s.solvingPlayer),
			s.game.RackFor(1-s.solvingPlayer),
			false, s.game.ScorelessTurns(),
		)
	}
	α := -HugeNumber
	β := HugeNumber
	if s.firstWinOptim {
		// Search a very small window centered around 0. We're just trying
		// to find something that surpasses it.
		α = -1
		β = 1
	}
	s.currentIDDepths = make([]int, 1) // a hack
	pv := PVLine{g: s.game}
	val, err := s.negamax(ctx, initialHashKey, plies, α, β, &pv, 0)
	s.principalVariation = pv
	s.bestPVValue = val - int16(s.initialSpread)

	bestSeq = s.principalVariation.Moves[:s.principalVariation.numMoves]
	bestV = s.bestPVValue
	log.Debug().
		Int("thread", thread).
		Uint64("ttable-created", s.ttable.created.Load()).
		Uint64("ttable-lookups", s.ttable.lookups.Load()).
		Uint64("ttable-hits", s.ttable.hits.Load()).
		Uint64("ttable-t2collisions", s.ttable.t2collisions.Load()).
		Float64("time-elapsed-sec", time.Since(tstart).Seconds()).
		Int16("bestV", bestV).
		Str("bestSeq", s.principalVariation.NLBString()).
		Msg("solve-returning")

	return bestV, bestSeq, err
}

func (s *Solver) SetIterativeDeepening(id bool) {
	s.iterativeDeepeningOptim = id
}

func (s *Solver) SetTranspositionTableOptim(tt bool) {
	s.transpositionTableOptim = tt
}

func (s *Solver) SetFirstWinOptim(w bool) {
	s.firstWinOptim = w
}

func (s *Solver) IsSolving() bool {
	return s.busy
}

func (s *Solver) Game() *game.Game {
	return s.game
}
