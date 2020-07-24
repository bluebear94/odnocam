package alphabet

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/dgryski/go-pcgr"
	"github.com/domino14/macondo/config"
	"github.com/matryer/is"
)

var randSource = pcgr.New(time.Now().UnixNano(), 42)

var DefaultConfig = config.Config{
	StrategyParamsPath:        os.Getenv("STRATEGY_PARAMS_PATH"),
	LetterDistributionPath:    os.Getenv("LETTER_DISTRIBUTION_PATH"),
	LexiconPath:               os.Getenv("LEXICON_PATH"),
	DefaultLexicon:            "NWL18",
	DefaultLetterDistribution: "English",
}

func TestBag(t *testing.T) {
	ld, err := EnglishLetterDistribution(&DefaultConfig)
	if err != nil {
		t.Error(err)
	}
	bag := ld.MakeBag(&randSource)
	if bag.numTiles != ld.numLetters {
		t.Error("Tile bag and letter distribution do not match.")
	}
	tileMap := make(map[rune]uint8)
	numTiles := 0
	for i := 0; i < bag.initialNumTiles; i++ {
		tiles, err := bag.Draw(1)
		if err != nil {
			t.Error(err)
		}
		numTiles++
		uv := tiles[0].UserVisible(ld.Alphabet())
		t.Logf("Drew a %c!, %v (in bag %v)", uv, numTiles, bag.numTiles)
		if err != nil {
			t.Error("Error drawing from tile bag.")
		}
		tileMap[uv]++
	}
	if !reflect.DeepEqual(tileMap, ld.Distribution) {
		t.Error("Distribution and tilemap were not identical.")
	}
	_, err = bag.Draw(1)
	if err == nil {
		t.Error("Should not have been able to draw from an empty bag.")
	}
}

func TestDraw(t *testing.T) {
	ld, err := EnglishLetterDistribution(&DefaultConfig)
	if err != nil {
		t.Error(err)
	}
	bag := ld.MakeBag(&randSource)

	letters, _ := bag.Draw(7)
	if len(letters) != 7 {
		t.Errorf("Length was %v, expected 7", len(letters))
	}
	if bag.numTiles != 93 {
		t.Errorf("Length was %v, expected 93", bag.numTiles)
	}
}

func TestDrawAtMost(t *testing.T) {
	ld, err := EnglishLetterDistribution(&DefaultConfig)
	if err != nil {
		t.Error(err)
	}
	bag := ld.MakeBag(&randSource)

	for i := 0; i < 14; i++ {
		letters, _ := bag.Draw(7)
		if len(letters) != 7 {
			t.Errorf("Length was %v, expected 7", len(letters))
		}
	}
	if bag.TilesRemaining() != 2 {
		t.Errorf("TilesRemaining was %v, expected 2", bag.TilesRemaining())
	}
	letters := bag.DrawAtMost(7)
	if len(letters) != 2 {
		t.Errorf("Length was %v, expected 2", len(letters))
	}
	if bag.TilesRemaining() != 0 {
		t.Errorf("TilesRemaining was %v, expected 0", bag.TilesRemaining())
	}
	// Try to draw one more time.
	letters = bag.DrawAtMost(7)
	if len(letters) != 0 {
		t.Errorf("Length was %v, expected 0", len(letters))
	}
	if bag.TilesRemaining() != 0 {
		t.Errorf("TilesRemaining was %v, expected 0", bag.TilesRemaining())
	}
}

func TestExchange(t *testing.T) {
	is := is.New(t)
	ld, err := EnglishLetterDistribution(&DefaultConfig)
	if err != nil {
		t.Error(err)
	}
	bag := ld.MakeBag(&randSource)

	letters, _ := bag.Draw(7)
	newLetters, _ := bag.Exchange(letters[:5])
	is.Equal(len(newLetters), 5)
	is.Equal(bag.numTiles, 93)
}

func TestRemoveTiles(t *testing.T) {
	is := is.New(t)
	ld, err := EnglishLetterDistribution(&DefaultConfig)
	if err != nil {
		t.Error(err)
	}
	bag := ld.MakeBag(&randSource)
	is.Equal(bag.numTiles, 100)
	toRemove := []MachineLetter{
		9, 14, 24, 4, 3, 20, 4, 11, 21, 6, 22, 14, 8, 0, 8, 15, 6, 5, 4,
		19, 0, 24, 8, 17, 17, 18, 2, 11, 8, 14, 1, 8, 0, 20, 7, 0, 8, 10,
		0, 11, 13, 25, 11, 14, 5, 8, 19, 4, 12, 8, 18, 4, 3, 19, 14, 19,
		1, 0, 13, 4, 19, 14, 4, 17, 20, 6, 21, 104, 3, 7, 0, 3, 14, 22,
		4, 8, 13, 16, 20, 4, 18, 19, 4, 23, 4, 2, 17, 12, 14, 0, 13,
	}
	is.Equal(len(toRemove), 91)
	err = bag.RemoveTiles(toRemove)
	if err != nil {
		t.Error(err)
	}
	is.Equal(bag.numTiles, 9)
}

func TestDrawTileAt(t *testing.T) {
	is := is.New(t)
	ld, err := EnglishLetterDistribution(&DefaultConfig)
	if err != nil {
		t.Error(err)
	}
	bag := ld.MakeBag(&randSource)

	tile, err := bag.drawTileAt(0)
	is.NoErr(err)
	is.Equal(MachineLetter(0), tile)
	is.Equal(bag.numTiles, 99)

	tile, err = bag.drawTileAt(99)
	is.Equal(MachineLetter(0), tile)
	is.Equal(err, errors.New("tile index out of range"))

	tile, err = bag.drawTileAt(98)
	is.Equal(MachineLetter(BlankMachineLetter), tile)
	is.NoErr(err)

	tile, err = bag.drawTileAt(8)
	is.Equal(MachineLetter(1), tile)
	is.NoErr(err)

	tile, err = bag.drawTileAt(8)
	is.Equal(MachineLetter(1), tile)
	is.NoErr(err)

	tile, err = bag.drawTileAt(8)
	is.Equal(MachineLetter(2), tile)
	is.NoErr(err)

	// is.Equal(MachineLetter(BlankMachineLetter), bag.drawTileAt(99))
	// is.Equal(MachineLetter(BlankMachineLetter), bag.drawTileAt(98))

}
