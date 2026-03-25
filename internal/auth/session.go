package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const apiBaseURL = "https://stake-engine.com/api"

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Image string `json:"image"`
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func ValidateSession(sid string) (*User, error) {
	req, err := http.NewRequest("GET", apiBaseURL+"/users", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "sid", Value: sid})

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrSessionExpired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if user.ID == "" {
		return nil, fmt.Errorf("empty user ID in response")
	}

	return &user, nil
}
