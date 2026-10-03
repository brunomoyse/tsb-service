package auth

import "testing"

func TestParseZitadelUserInfo(t *testing.T) {
	tests := []struct {
		name                 string
		body                 string
		email, given, family string
		wantErr              bool
	}{
		{
			name:   "human",
			body:   `{"user":{"userId":"1","username":"jane@example.com","human":{"profile":{"givenName":"Jane","familyName":"Doe"},"email":{"email":"jane@example.com"}}}}`,
			email:  "jane@example.com",
			given:  "Jane",
			family: "Doe",
		},
		{
			name:   "machine user gets placeholder email",
			body:   `{"user":{"userId":"2","username":"TSB-MCP","machine":{"name":"TSB MCP server"}}}`,
			email:  "tsb-mcp@machine.invalid",
			given:  "TSB MCP server",
			family: "bot",
		},
		{
			name:   "machine user without display name",
			body:   `{"user":{"userId":"3","username":"bot","machine":{}}}`,
			email:  "bot@machine.invalid",
			given:  "bot",
			family: "bot",
		},
		{
			name: "neither human nor machine",
			body: `{"user":{"userId":"4"}}`,
		},
		{
			name:    "invalid json",
			body:    `{`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, given, family, err := parseZitadelUserInfo([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if email != tt.email || given != tt.given || family != tt.family {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)", email, given, family, tt.email, tt.given, tt.family)
			}
		})
	}
}
