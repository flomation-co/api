package persistence

import "testing"

// TestSearchAgentMessages_Guards verifies the scope/query guards short-circuit
// before any DB access, so a missing agent/user/query can never run an
// unscoped query or leak another user's history.
func TestSearchAgentMessages_Guards(t *testing.T) {
	var s Service // zero value: s.conn is nil, so reaching it would panic
	cases := []struct{ agent, user, query string }{
		{"", "u", "hello"},
		{"a", "", "hello"},
		{"a", "u", ""},
		{"a", "u", "   "},
	}
	for _, c := range cases {
		res, err := s.SearchAgentMessages(c.agent, c.user, c.query, 5)
		if err != nil || res != nil {
			t.Errorf("SearchAgentMessages(%q,%q,%q) = %v, %v; want nil, nil", c.agent, c.user, c.query, res, err)
		}
	}
}
