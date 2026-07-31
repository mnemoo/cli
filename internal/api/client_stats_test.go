package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGameStatsAndGraphUsePortalContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if cookie, err := request.Cookie("sid"); err != nil || cookie.Value != "session" {
			t.Errorf("missing session cookie: %v", err)
		}
		switch request.URL.Path {
		case "/teams/team/games/game/stats":
			if request.URL.Query().Has("start") {
				query := request.URL.Query()
				if query.Get("operator") != "stake,stake_us,stake_mx" || query.Get("start") != "100" || query.Get("end") != "200" {
					t.Errorf("unexpected stats query: %s", request.URL.RawQuery)
				}
			}
			_, _ = response.Write([]byte(`{"name":"Game","slug":"game","stats":[{"mode":"base","count":1,"turnover":100000000,"profit":5000000,"expectedReturn":4000000,"rtp":0.95}]}`))
		case "/teams/team/teamStats":
			query := request.URL.Query()
			if query.Get("operator") != "stake,stake_us,stake_mx" || query.Get("start") != "100" || query.Get("end") != "200" {
				t.Errorf("unexpected team stats query: %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`[{"betCount":1,"turnover":100000000,"profit":5000000,"expectedProfit":4000000,"uniqueUsers":1,"operator":"stake"}]`))
		case "/teams/team/payments/method":
			_, _ = response.Write([]byte(`{"current":"profit","next":"turnover"}`))
		case "/teams/team/graph":
			query := request.URL.Query()
			if query.Get("game") != "game" || query.Get("operator") != "stake,stake_us,stake_mx" || query.Get("start") != "100" || query.Get("end") != "200" {
				t.Errorf("unexpected graph query: %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"buckets":["100"],"profit":[5000000],"turnover":[100000000],"count":[1]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	previous := baseURL
	baseURL = server.URL
	defer func() { baseURL = previous }()

	client := NewClient("session")
	stats, err := client.GetGameStats(context.Background(), "team", "game")
	if err != nil || len(stats.Stats) != 1 || stats.Stats[0].ExpectedReturn != 4_000_000 {
		t.Fatalf("get game stats: stats=%#v err=%v", stats, err)
	}
	rangedStats, err := client.GetGameStatsRange(context.Background(), "team", "game", time.Unix(100, 0), time.Unix(200, 0))
	if err != nil || len(rangedStats.Stats) != 1 {
		t.Fatalf("get ranged game stats: stats=%#v err=%v", rangedStats, err)
	}
	teamStats, err := client.GetTeamStats(context.Background(), "team", time.Unix(100, 0), time.Unix(200, 0))
	if err != nil || len(teamStats) != 1 || teamStats[0].ExpectedProfit != 4_000_000 {
		t.Fatalf("get team stats: stats=%#v err=%v", teamStats, err)
	}
	method, err := client.GetPaymentMethod(context.Background(), "team")
	if err != nil || method.Current != "profit" || method.Next != "turnover" {
		t.Fatalf("get payment method: method=%#v err=%v", method, err)
	}
	graph, err := client.GetGameGraph(context.Background(), "team", "game", time.Unix(100, 0), time.Unix(200, 0))
	if err != nil || len(graph.Profit) != 1 || graph.Profit[0] != 5_000_000 {
		t.Fatalf("get game graph: graph=%#v err=%v", graph, err)
	}
}
