package puzzles

import (
	"github.com/domino14/macondo/ai/runner"
	"github.com/domino14/macondo/config"
	"github.com/domino14/macondo/game"
	pb "github.com/domino14/macondo/gen/api/proto/macondo"
)

func CreatePuzzlesFromGame(conf *config.Config, g *game.Game) ([]*pb.PuzzleResponse, error) {
	evts := g.History().Events
	puzzles := []*pb.PuzzleResponse{}
	for evtIdx := range evts {
		g.PlayToTurn(evtIdx)
		runner, err := runner.NewAIGameRunnerFromGame(g, conf, pb.BotRequest_HASTY_BOT)
		if err != nil {
			return nil, err
		}
		moves := runner.GenerateMoves(2)
		if len(moves) == 2 && moves[0].Equity() > moves[1].Equity()+10 {
			puzzles = append(puzzles, &pb.PuzzleResponse{GameId: g.Uid(),
				TurnNumber: int32(evtIdx),
				Answer:     g.EventFromMove(moves[0])})
		}
	}
	return puzzles, nil
}
