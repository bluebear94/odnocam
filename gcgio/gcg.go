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

	"github.com/domino14/macondo/game"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	pb "github.com/domino14/macondo/gen/api/proto/macondo"
	"github.com/rs/zerolog/log"
)

var (
	errDuplicateNames     = errors.New("two players with same nickname not supported")
	errPragmaPrecedeEvent = errors.New("non-note pragmata should appear before event lines")
	errEncodingWrongPlace = errors.New("encoding line must be first line in file if present")
	errPlayerNotSupported = errors.New("player number not supported")
)

// A Token is an event in a GCG file.
type Token uint8

const (
	UndefinedToken Token = iota
	PlayerToken
	TitleToken
	DescriptionToken
	IDToken
	Rack1Token
	Rack2Token
	EncodingToken
	MoveToken
	NoteToken
	LexiconToken
	PhonyTilesReturnedToken
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
	TitleRegex              = `#title\s*(?P<title>.*)`
	DescriptionRegex        = `#description\s*(?P<description>.*)`
	IDRegex                 = `#id\s*(?P<id_authority>\S+)\s+(?P<id>\S+)`
	Rack1Regex              = `#rack1 (?P<rack>\S+)`
	Rack2Regex              = `#rack2 (?P<rack>\S+)`
	MoveRegex               = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+(?P<pos>\w+)\s+(?P<play>[\w\\.]+)\s+\+(?P<score>\d+)\s+(?P<cumul>\d+)`
	NoteRegex               = `#note (?P<note>.+)`
	LexiconRegex            = `#lexicon (?P<lexicon>.+)`
	CharacterEncodingRegex  = `#character-encoding (?P<encoding>[[:graph:]]+)`
	PhonyTilesReturnedRegex = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+--\s+-(?P<lost_score>\d+)\s+(?P<cumul>\d+)`
	PassRegex               = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+-\s+\+0\s+(?P<cumul>\d+)`
	ChallengeBonusRegex     = `>(?P<nick>\S+):\s+(?P<rack>\S*)\s+\(challenge\)\s+\+(?P<bonus>\d+)\s+(?P<cumul>\d+)`
	ExchangeRegex           = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+-(?P<exchanged>\S+)\s+\+0\s+(?P<cumul>\d+)`
	EndRackPointsRegex      = `>(?P<nick>\S+):\s+\((?P<rack>\S+)\)\s+\+(?P<score>\d+)\s+(?P<cumul>-?\d+)`
	TimePenaltyRegex        = `>(?P<nick>\S+):\s+(?P<rack>\S*)\s+\(time\)\s+\-(?P<penalty>\d+)\s+(?P<cumul>-?\d+)`
	PtsLostForLastRackRegex = `>(?P<nick>\S+):\s+(?P<rack>\S+)\s+\((?P<rack>\S+)\)\s+\-(?P<penalty>\d+)\s+(?P<cumul>-?\d+)`
)

var compiledEncodingRegexp *regexp.Regexp

type parser struct {
	lastToken Token

	history *pb.GameHistory
	game    *game.Game
}

// init initializes the regexp list.
func init() {
	// Important note: ChallengeBonusRegex is defined BEFORE EndRackPointsRegex.
	// That is because a line like  `>frentz:  (challenge) +5 534`  matches
	// both regexes. This can probably be avoided by being more strict about
	// what type of characters the rack can be, etc.

	compiledEncodingRegexp = regexp.MustCompile(CharacterEncodingRegex)

	GCGRegexes = []gcgdatum{
		{PlayerToken, regexp.MustCompile(PlayerRegex)},
		{TitleToken, regexp.MustCompile(TitleRegex)},
		{DescriptionToken, regexp.MustCompile(DescriptionRegex)},
		{IDToken, regexp.MustCompile(IDRegex)},
		{Rack1Token, regexp.MustCompile(Rack1Regex)},
		{Rack2Token, regexp.MustCompile(Rack2Regex)},
		{EncodingToken, compiledEncodingRegexp},
		{MoveToken, regexp.MustCompile(MoveRegex)},
		{NoteToken, regexp.MustCompile(NoteRegex)},
		{LexiconToken, regexp.MustCompile(LexiconRegex)},
		{PhonyTilesReturnedToken, regexp.MustCompile(PhonyTilesReturnedRegex)},
		{PassToken, regexp.MustCompile(PassRegex)},
		{ChallengeBonusToken, regexp.MustCompile(ChallengeBonusRegex)},
		{ExchangeToken, regexp.MustCompile(ExchangeRegex)},
		{EndRackPointsToken, regexp.MustCompile(EndRackPointsRegex)},
		{TimePenaltyToken, regexp.MustCompile(TimePenaltyRegex)},
		{LastRackPenaltyToken, regexp.MustCompile(PtsLostForLastRackRegex)},
	}
}

func matchToInt32(str string) (int32, error) {
	x, err := strconv.ParseInt(str, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(x), nil
}

func (p *parser) addEventOrPragma(token Token, match []string) error {
	var err error

	switch token {
	case PlayerToken:
		if len(p.history.Turns) > 0 {
			return errPragmaPrecedeEvent
		}
		pn, err := strconv.Atoi(match[1])
		if err != nil {
			return err
		}
		if pn != 1 && pn != 2 {
			return errPlayerNotSupported
		}
		if pn == 2 {
			if match[2] == p.history.Players[0].Nickname {
				return errDuplicateNames
			}
		}

		p.history.Players = append(p.history.Players, &pb.PlayerInfo{
			Nickname: match[2],
			RealName: match[3],
		})

		return nil
	case TitleToken:
		if len(p.history.Turns) > 0 {
			return errPragmaPrecedeEvent
		}
		p.history.Title = match[1]
		return nil
	case DescriptionToken:
		if len(p.history.Turns) > 0 {
			return errPragmaPrecedeEvent
		}
		p.history.Description = match[1]
	case IDToken:
		if len(p.history.Turns) > 0 {
			return errPragmaPrecedeEvent
		}
		p.history.IdAuth = match[1]
		p.history.Uid = match[2]
	// Assume Rack1Token always comes before Rack2Token in a well-formed gcg:
	case Rack1Token:
		p.history.LastKnownRacks = []string{match[1]}
	case Rack2Token:
		p.history.LastKnownRacks = append(p.history.LastKnownRacks, match[1])
	case EncodingToken:
		return errEncodingWrongPlace
	case MoveToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		evt.Position = match[3]
		evt.PlayedTiles = match[4]
		evt.Score, err = matchToInt32(match[5])
		if err != nil {
			return err
		}
		evt.Cumulative, err = matchToInt32(match[6])
		if err != nil {
			return err
		}
		game.CalculateCoordsFromStringPosition(evt)
		evt.Type = pb.GameEvent_TILE_PLACEMENT_MOVE
		evts := []*pb.GameEvent{evt}
		turn := &pb.GameTurn{Events: evts}

		p.history.Turns = append(p.history.Turns, turn)

	case NoteToken:
		lastTurnIdx := len(p.history.Turns) - 1
		lastEvtIdx := len(p.history.Turns[lastTurnIdx].Events) - 1
		p.history.Turns[lastTurnIdx].Events[lastEvtIdx].Note += match[1]
		return nil
	case LexiconToken:
		if len(p.history.Turns) > 0 {
			return errPragmaPrecedeEvent
		}
		p.history.Lexicon = match[1]
		return nil
	case PhonyTilesReturnedToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]

		score, err := matchToInt32(match[3])
		if err != nil {
			return err
		}
		evt.LostScore = score
		evt.Cumulative, err = matchToInt32(match[4])
		if err != nil {
			return err
		}
		// This can not be a stand-alone turn; it must be added to the previous
		// turn.
		lastTurnIdx := len(p.history.Turns) - 1
		p.history.Turns[lastTurnIdx].Events = append(p.history.Turns[lastTurnIdx].Events, evt)
		evt.Type = pb.GameEvent_PHONY_TILES_RETURNED

	case TimePenaltyToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]

		score, err := matchToInt32(match[3])
		if err != nil {
			return err
		}
		evt.LostScore = score
		evt.Cumulative, err = matchToInt32(match[4])
		if err != nil {
			return err
		}
		// Treat this as a stand-alone turn; it should not be attached to
		// the previous event because it can occur after the wrong player
		// (i.e. player2 goes out, and then time penalty is applied to player1)

		evt.Type = pb.GameEvent_TIME_PENALTY
		evts := []*pb.GameEvent{evt}
		turn := &pb.GameTurn{Events: evts}
		p.history.Turns = append(p.history.Turns, turn)

	case LastRackPenaltyToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		if evt.Rack != match[3] {
			return fmt.Errorf("last rack penalty event malformed")
		}
		score, err := matchToInt32(match[4])
		if err != nil {
			return err
		}
		evt.LostScore = score
		evt.Cumulative, err = matchToInt32(match[5])
		if err != nil {
			return err
		}
		evt.Type = pb.GameEvent_END_RACK_PENALTY
		evts := []*pb.GameEvent{evt}
		turn := &pb.GameTurn{Events: evts}
		p.history.Turns = append(p.history.Turns, turn)

	case PassToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		evt.Cumulative, err = matchToInt32(match[3])
		if err != nil {
			return err
		}
		evt.Type = pb.GameEvent_PASS
		evts := []*pb.GameEvent{evt}
		turn := &pb.GameTurn{Events: evts}
		p.history.Turns = append(p.history.Turns, turn)

	case ChallengeBonusToken, EndRackPointsToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		if token == ChallengeBonusToken {
			evt.Bonus, err = matchToInt32(match[3])
		} else {
			evt.EndRackPoints, err = matchToInt32(match[3])
		}
		if err != nil {
			return err
		}
		evt.Cumulative, err = matchToInt32(match[4])
		if err != nil {
			return err
		}
		if token == ChallengeBonusToken {
			evt.Type = pb.GameEvent_CHALLENGE_BONUS
		} else if token == EndRackPointsToken {
			evt.Type = pb.GameEvent_END_RACK_PTS
		}
		lastTurnIdx := len(p.history.Turns) - 1
		p.history.Turns[lastTurnIdx].Events = append(p.history.Turns[lastTurnIdx].Events, evt)

	case ExchangeToken:
		evt := &pb.GameEvent{}
		evt.Nickname = match[1]
		evt.Rack = match[2]
		evt.Exchanged = match[3]
		evt.Cumulative, err = matchToInt32(match[4])
		if err != nil {
			return err
		}
		evt.Type = pb.GameEvent_EXCHANGE
		evts := []*pb.GameEvent{evt}
		turn := &pb.GameTurn{Events: evts}
		p.history.Turns = append(p.history.Turns, turn)

	}
	return nil
}

func (p *parser) parseLine(line string) error {

	foundMatch := false

	for _, datum := range GCGRegexes {
		match := datum.regex.FindStringSubmatch(line)
		if match != nil {
			foundMatch = true
			err := p.addEventOrPragma(datum.token, match)
			if err != nil {
				return err
			}
			p.lastToken = datum.token
			break
		}
	}
	if !foundMatch {
		// maybe it's a multi-line note.
		if p.lastToken == NoteToken {
			lastTurnIdx := len(p.history.Turns) - 1
			lastEventIdx := len(p.history.Turns[lastTurnIdx].Events) - 1
			p.history.Turns[lastTurnIdx].Events[lastEventIdx].Note += ("\n" + line)
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

func ParseGCGFromReader(reader io.Reader) (*pb.GameHistory, error) {

	var err error
	parser := &parser{
		history: &pb.GameHistory{
			Turns:   []*pb.GameTurn{},
			Players: []*pb.PlayerInfo{},
			Version: 1},
	}
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
		err = parser.parseLine(firstLine)
		if err != nil {
			return nil, err
		}
		originalGCG += firstLine + "\n"
	}

	for scanner.Scan() {
		line := scanner.Text()
		err = parser.parseLine(line)
		if err != nil {
			return nil, err
		}
		originalGCG += line + "\n"
	}
	parser.history.OriginalGcg = strings.TrimSpace(originalGCG)
	return parser.history, nil
}

// ParseGCG parses a GCG file into a GameHistory.
func ParseGCG(filename string) (*pb.GameHistory, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	return ParseGCGFromReader(f)
}

func writeGCGHeader(s *strings.Builder, h *pb.GameHistory, addlInfo bool) {
	s.WriteString("#character-encoding UTF-8\n")
	if addlInfo {
		if h.Title != "" {
			s.WriteString("#title " + h.Title + "\n")
		}
		if h.Description != "" {
			s.WriteString("#description " + h.Description + "\n")
		}
		if h.IdAuth != "" && h.Uid != "" {
			s.WriteString("#id " + h.IdAuth + " " + h.Uid + "\n")
		}
	}
	log.Debug().Msg("wrote header")
}

func writeEvent(s *strings.Builder, evt *pb.GameEvent) error {

	nick := evt.GetNickname()
	rack := evt.GetRack()
	evtType := evt.GetType()
	note := evt.GetNote()

	// XXX HANDLE MORE TYPES (e.g. time penalty at some point, end rack
	// penalty for international rules)
	switch evtType {
	case pb.GameEvent_TILE_PLACEMENT_MOVE:
		fmt.Fprintf(s, ">%v: %v %v %v +%d %d\n",
			nick, rack, evt.Position, evt.PlayedTiles, evt.Score, evt.Cumulative,
		)
	case pb.GameEvent_PHONY_TILES_RETURNED:
		// >emely: DEIILTZ -- -24 55
		fmt.Fprintf(s, ">%v: %v -- -%d %d\n",
			nick, rack, evt.LostScore, evt.Cumulative)

	case pb.GameEvent_PASS:
		// >Randy: U - +0 380
		fmt.Fprintf(s, ">%v: %v - +0 %d\n", nick, rack, evt.Cumulative)
	case pb.GameEvent_CHALLENGE_BONUS:
		// >Joel: DROWNUG (challenge) +5 289
		fmt.Fprintf(s, ">%v: %v (challenge) +%d %d\n",
			nick, rack, evt.Bonus, evt.Cumulative)

	case pb.GameEvent_END_RACK_PTS:
		// >Dave: (G) +4 539
		fmt.Fprintf(s, ">%v: (%v) +%d %d\n",
			nick, rack, evt.EndRackPoints, evt.Cumulative)

	case pb.GameEvent_EXCHANGE:
		// >Marlon: SEQSPO? -QO +0 268
		fmt.Fprintf(s, ">%v: %v -%v +0 %d\n",
			nick, rack, evt.Exchanged, evt.Cumulative)

	case pb.GameEvent_END_RACK_PENALTY:
		// >Pakorn: FWLI (FWLI) -10 426
		fmt.Fprintf(s, ">%v: %v (%v) -%d %d\n",
			nick, rack, rack, evt.LostScore, evt.Cumulative)
	case pb.GameEvent_TIME_PENALTY:
		// >Pakorn: ISBALI (time) -10 409
		fmt.Fprintf(s, ">%v: %v (time) -%d %d\n",
			nick, rack, evt.LostScore, evt.Cumulative)

	default:
		return fmt.Errorf("event type %v not supported", evtType)

	}
	if note != "" {
		// Note that the note can have line breaks within it ...
		fmt.Fprintf(s, "#note %v\n", note)
	}
	return nil

}

func writeTurn(s *strings.Builder, t *pb.GameTurn) error {
	for _, evt := range t.Events {
		err := writeEvent(s, evt)
		if err != nil {
			return err
		}
	}
	return nil
}

func writePlayer(s *strings.Builder, pn int, p *pb.PlayerInfo) {
	fmt.Fprintf(s, "#player%d %v %v\n", pn, p.Nickname, p.RealName)
}

func writePlayers(s *strings.Builder, players []*pb.PlayerInfo, flip bool) {
	if flip {
		writePlayer(s, 1, players[1])
		writePlayer(s, 2, players[0])
	} else {
		writePlayer(s, 1, players[0])
		writePlayer(s, 2, players[1])
	}
}

// GameHistoryToGCG returns a string GCG representation of the GameHistory.
func GameHistoryToGCG(h *pb.GameHistory, addlHeaderInfo bool) (string, error) {

	var str strings.Builder
	writeGCGHeader(&str, h, addlHeaderInfo)
	writePlayers(&str, h.Players, h.FlipPlayers)

	for _, turn := range h.Turns {
		err := writeTurn(&str, turn)
		if err != nil {
			return "", err
		}
	}

	return str.String(), nil
}
