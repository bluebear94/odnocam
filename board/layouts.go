package board

var (
	// CrosswordGameBoard is a board for a fun Crossword Game, featuring lots
	// of wingos and blonks.
	CrosswordGameBoard []string
	// CrosswordGameBoardGmo is a version of CrosswordGameBoard
	// without the center 2WS.
	CrosswordGameBoardGmo []string
	// SuperCrosswordGameBoard is a board for a bigger Crossword game, featuring
	// even more wingos and blonks.
	SuperCrosswordGameBoard []string
)

const (
	CrosswordGameLayout      = "CrosswordGame"
	CrosswordGameLayoutGmo   = "CrosswordGameGmo"
	SuperCrosswordGameLayout = "SuperCrosswordGame"
)

func init() {
	CrosswordGameBoard = []string{
		`=  '   =   '  =`,
		` -   "   "   - `,
		`  -   ' '   -  `,
		`'  -   '   -  '`,
		`    -     -    `,
		` "   "   "   " `,
		`  '   ' '   '  `,
		`=  '   -   '  =`,
		`  '   ' '   '  `,
		` "   "   "   " `,
		`    -     -    `,
		`'  -   '   -  '`,
		`  -   ' '   -  `,
		` -   "   "   - `,
		`=  '   =   '  =`,
	}
	CrosswordGameBoardGmo = []string{
		`=  '   =   '  =`,
		` -   "   "   - `,
		`  -   ' '   -  `,
		`'  -   '   -  '`,
		`    -     -    `,
		` "   "   "   " `,
		`  '   ' '   '  `,
		`=  '       '  =`,
		`  '   ' '   '  `,
		` "   "   "   " `,
		`    -     -    `,
		`'  -   '   -  '`,
		`  -   ' '   -  `,
		` -   "   "   - `,
		`=  '   =   '  =`,
	}
	SuperCrosswordGameBoard = []string{
		`~  '   =  '  =   '  ~`,
		` -  "   -   -   "  - `,
		`  -  ^   - -   ^  -  `,
		`'  =  '   =   '  =  '`,
		` "  -   "   "   -  " `,
		`  ^  -   ' '   -  ^  `,
		`   '  -   '   -  '   `,
		`=      -     -      =`,
		` -  "   "   "   "  - `,
		`  -  '   ' '   '  -  `,
		`'  =  '   -   '  =  '`,
		`  -  '   ' '   '  -  `,
		` -  "   "   "   "  - `,
		`=      -     -      =`,
		`   '  -   '   -  '   `,
		`  ^  -   ' '   -  ^  `,
		` "  -   "   "   -  " `,
		`'  =  '   =   '  =  '`,
		`  -  ^   - -   ^  -  `,
		` -  "   -   -   "  - `,
		`~  '   =  '  =   '  ~`,
	}
}
