package zobrist

import (
	"lukechampine.com/frand"

	"github.com/domino14/macondo/game"
	"github.com/domino14/macondo/move"
	"github.com/domino14/macondo/tilemapping"
)

const bignum = 1<<63 - 2

// generate a zobrist hash for a crossword game position.
// https://en.wikipedia.org/wiki/Zobrist_hashing
type Zobrist struct {
	minimizingPlayerToMove uint64

	posTable       [][]uint64
	maxRackTable   [][]uint64 // rack for the maximizing player
	minRackTable   [][]uint64 // rack for the minimizing player
	scorelessTurns [3]uint64

	boardDim        int
	placeholderRack []tilemapping.MachineLetter
}

func (z *Zobrist) Initialize(boardDim int) {
	z.boardDim = boardDim
	z.posTable = make([][]uint64, boardDim*boardDim)
	for i := 0; i < boardDim*boardDim; i++ {

		// 200 is MaxAlphabetSize + 0x80 (blank) + some fudge factor.
		// This is kind of ugly; we should clarify the domain a bit.
		// We don't lose a huge deal by generating this many random numbers,
		// however, even if we're not using all of them.
		z.posTable[i] = make([]uint64, 200)
		for j := 0; j < 200; j++ {
			z.posTable[i][j] = frand.Uint64n(bignum) + 1
		}
	}
	z.maxRackTable = make([][]uint64, tilemapping.MaxAlphabetSize+1)
	for i := 0; i < tilemapping.MaxAlphabetSize+1; i++ {
		z.maxRackTable[i] = make([]uint64, game.RackTileLimit)
		for j := 0; j < game.RackTileLimit; j++ {
			z.maxRackTable[i][j] = frand.Uint64n(bignum) + 1
		}
	}
	z.minRackTable = make([][]uint64, tilemapping.MaxAlphabetSize+1)
	for i := 0; i < tilemapping.MaxAlphabetSize+1; i++ {
		z.minRackTable[i] = make([]uint64, game.RackTileLimit)
		for j := 0; j < game.RackTileLimit; j++ {
			z.minRackTable[i][j] = frand.Uint64n(bignum) + 1
		}
	}

	for i := 0; i < 3; i++ {
		z.scorelessTurns[i] = frand.Uint64n(bignum) + 1
	}

	z.minimizingPlayerToMove = frand.Uint64n(bignum) + 1
	z.placeholderRack = make([]tilemapping.MachineLetter, tilemapping.MaxAlphabetSize+1)
}

func (z *Zobrist) Hash(squares tilemapping.MachineWord, maxPlayerRack *tilemapping.Rack,
	minPlayerRack *tilemapping.Rack, minimizingPlayerToMove bool) uint64 {

	key := uint64(0)
	for i, letter := range squares {
		if letter == 0 {
			continue
		}
		key ^= z.posTable[i][letter]
	}
	for i, ct := range maxPlayerRack.LetArr {
		key ^= z.maxRackTable[i][ct]
	}
	for i, ct := range minPlayerRack.LetArr {
		key ^= z.minRackTable[i][ct]
	}
	if minimizingPlayerToMove {
		key ^= z.minimizingPlayerToMove
	}
	// assume 0 scoreless turns at the beginning
	key ^= z.scorelessTurns[0]
	return key
}

func (z *Zobrist) AddMove(key uint64, m move.PlayMaker, maxPlayer bool, scorelessTurns, lastScorelessTurns int) uint64 {
	// Adding a move:
	// For every letter in the move (assume it's only a tile placement move
	// or a pass for now):
	// - XOR with its position on the board
	// - XOR with the "position" on the rack hash
	// Then:
	// - XOR with p2ToMove since we always alternate
	// If it is a pass:
	// - XOR with the index at consecutivePass - 1
	// - XOR with the index at consecutivePass

	ourRackTable := z.maxRackTable
	if !maxPlayer {
		ourRackTable = z.minRackTable
	}
	if m.Type() == move.MoveTypePlay {
		row, col, vertical := m.RowStart(), m.ColStart(), m.Vertical()
		ri, ci := 0, 1
		if vertical {
			ri, ci = 1, 0
		}
		// clear out placeholder rack first:
		for i := 0; i < tilemapping.MaxAlphabetSize+1; i++ {
			z.placeholderRack[i] = 0
		}

		for idx, tile := range m.Tiles() {
			newRow := row + (ri * idx)
			newCol := col + (ci * idx)
			if tile == 0 {
				// 0 is a played-through marker if it's part of a move's tiles
				continue
			}
			key ^= z.posTable[newRow*z.boardDim+newCol][tile]
			// build up placeholder rack.
			tileIdx := tile.IntrinsicTileIdx()
			z.placeholderRack[tileIdx]++
		}
		for _, tile := range m.Leave() {
			z.placeholderRack[tile]++
		}
		// now "Play" all the tiles in the rack
		for _, tile := range m.Tiles() {
			if tile == 0 {
				continue
			}
			tileIdx := tile.IntrinsicTileIdx()
			key ^= ourRackTable[tileIdx][z.placeholderRack[tileIdx]]
			z.placeholderRack[tileIdx]--
			key ^= ourRackTable[tileIdx][z.placeholderRack[tileIdx]]

		}

	}
	key ^= z.scorelessTurns[lastScorelessTurns]
	key ^= z.scorelessTurns[scorelessTurns]

	key ^= z.minimizingPlayerToMove
	return key
}
