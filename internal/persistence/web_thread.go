package persistence

// WebThreadTurn is one turn of a web-invoke conversation thread.
type WebThreadTurn struct {
	Role    string `db:"role" json:"role"`
	Content string `db:"content" json:"content"`
}

// CreateWebThread mints a new thread for a flow, optionally bound to a logged-in
// user (nil ⇒ anonymous). Returns the new thread id.
func (s *Service) CreateWebThread(flowID string, userID *string) (string, error) {
	var id string
	err := s.conn.Get(&id, `INSERT INTO web_thread (flow_id, user_id) VALUES ($1, $2) RETURNING id`, flowID, userID)
	return id, err
}

// GetWebThreadHistory returns the most recent `limit` turns of a thread in
// chronological (oldest-first) order.
func (s *Service) GetWebThreadHistory(threadID string, limit int) ([]WebThreadTurn, error) {
	if limit <= 0 {
		limit = 20
	}
	turns := []WebThreadTurn{}
	err := s.conn.Select(&turns, `
		SELECT role, content FROM (
			SELECT role, content, created_at
			FROM web_thread_turn
			WHERE thread_id = $1
			ORDER BY created_at DESC
			LIMIT $2
		) recent
		ORDER BY created_at ASC`, threadID, limit)
	return turns, err
}

// AppendWebThreadTurn records one turn on a thread.
func (s *Service) AppendWebThreadTurn(threadID, role, content string) error {
	_, err := s.conn.Exec(
		`INSERT INTO web_thread_turn (thread_id, role, content) VALUES ($1, $2, $3)`,
		threadID, role, content,
	)
	return err
}
