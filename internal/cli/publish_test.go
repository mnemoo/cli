package cli

import "testing"

func TestParsePublishOptionsHeadlessFlagsAfterPositionals(t *testing.T) {
	options, help, err := parsePublishOptions([]string{
		"math", ".", "--headless", "--team", "my-team", "--game=my-game",
		"--compliance", "--base-mode", "base",
		"--update-approval-version", "--repack",
	})
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if help {
		t.Fatal("unexpected help")
	}
	if options.uploadType != "math" || options.path != "." || !options.headless || !options.checkCompliance || !options.updateApproval || !options.repack || options.team != "my-team" || options.game != "my-game" {
		t.Fatalf("unexpected options: %#v", options)
	}
	if options.compliance.BaseMode != "base" {
		t.Fatalf("unexpected compliance options: %#v", options.compliance)
	}
}

func TestParsePublishOptionsRestrictsRepackToHeadlessMath(t *testing.T) {
	if _, _, err := parsePublishOptions([]string{
		"math", ".", "--headless", "--team", "team", "--game", "game", "--repack",
	}); err != nil {
		t.Fatalf("headless math repack should be accepted: %v", err)
	}
	if _, _, err := parsePublishOptions([]string{"math", ".", "--repack"}); err == nil {
		t.Fatal("expected interactive math repack error")
	}
	if _, _, err := parsePublishOptions([]string{
		"front", ".", "--headless", "--team", "team", "--game", "game", "--repack",
	}); err == nil {
		t.Fatal("expected front repack error")
	}
}

func TestParsePublishOptionsInteractive(t *testing.T) {
	options, help, err := parsePublishOptions([]string{"front", "./dist"})
	if err != nil || help {
		t.Fatalf("parse options: help=%v err=%v", help, err)
	}
	if options.uploadType != "front" || options.path != "./dist" || options.headless {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParsePublishOptionsRequiresHeadlessTarget(t *testing.T) {
	compliance := []string{"--compliance", "--base-mode", "base"}
	if _, _, err := parsePublishOptions(append([]string{"math", ".", "--headless", "--team", "team"}, compliance...)); err == nil {
		t.Fatal("expected missing --game error")
	}
	if _, _, err := parsePublishOptions(append([]string{"math", ".", "--team", "team"}, compliance...)); err == nil {
		t.Fatal("expected interactive target override error")
	}
}

func TestParsePublishOptionsAllowsAutomaticTierAnalysis(t *testing.T) {
	if _, _, err := parsePublishOptions([]string{"math", ".", "--headless", "--team", "team", "--game", "game"}); err != nil {
		t.Fatalf("headless math should skip compliance by default: %v", err)
	}
	if _, _, err := parsePublishOptions([]string{"math", ".", "--headless", "--team", "team", "--game", "game", "--compliance"}); err != nil {
		t.Fatalf("headless compliance should analyze both tiers automatically: %v", err)
	}
	if _, _, err := parsePublishOptions([]string{"front", ".", "--compliance", "--base-mode", "base"}); err == nil {
		t.Fatal("expected front compliance flags error")
	}
}
