package main

import (
	"flag"
	"fmt"

	"github.com/tumour/awg-mesh/internal/jointoken"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdToken — перепечатывает join-token из существующего state, ничего не меняя.
// init печатает токен один раз; если вывод потерян — взять его заново негде
// (в файл токен не пишется). Эта команда собирает ту же строку из state.
//
// Работает на любой ноде, не только на seed: токен = cluster-secret +
// pubkey/endpoint seed'а, а cluster-secret общий для всех, и seed-инфо есть
// в peer-list каждого узла (разносится gossip'ом). Это согласуется с моделью
// «cluster-secret = доступ к mesh» (см. README, «Безопасность»).
func cmdToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	quiet := fs.Bool("quiet", false, "print only the bare token (for scripts)")
	fs.Parse(args)

	s, err := state.Load(*stateFlag)
	if err != nil {
		return err
	}

	seedPub, seedEndpoint, err := seedBootstrapInfo(s)
	if err != nil {
		return err
	}

	token, err := jointoken.Encode(jointoken.Token{
		Secret:       s.ClusterSecret,
		SeedPubKey:   seedPub,
		SeedEndpoint: seedEndpoint,
	})
	if err != nil {
		return fmt.Errorf("encode join-token: %w", err)
	}

	if *quiet {
		fmt.Println(token)
		return nil
	}

	fmt.Printf(`join-token (anyone with it can join this mesh — send via scp/ssh, not chat):

  meshd join --label <node-name> --token %s

seed: %s
`, token, seedEndpoint)
	return nil
}

// seedBootstrapInfo достаёт pubkey и endpoint seed'а — то, на что указывает
// join-token. На самом seed'е это наши данные; иначе ищем seed в peer-list.
func seedBootstrapInfo(s *state.State) (pubKey, endpoint string, err error) {
	if s.IsSeed {
		ep := selfEndpoint(s)
		if ep == "" {
			return "", "", fmt.Errorf("seed has no public endpoint in state — cannot build a usable token")
		}
		return s.PublicKey, ep, nil
	}
	// Предпочитаем seed С endpoint: если seed'ов несколько и у первого
	// endpoint пуст, не падаем — берём следующий пригодный.
	sawSeed := false
	for _, p := range s.Peers {
		if !p.IsSeed {
			continue
		}
		sawSeed = true
		if p.Endpoint != "" {
			return p.PublicKey, p.Endpoint, nil
		}
	}
	if sawSeed {
		return "", "", fmt.Errorf("known seed(s) have no endpoint in state — cannot build a usable token")
	}
	return "", "", fmt.Errorf("no seed found in state — cannot build a join-token")
}
