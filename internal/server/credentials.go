package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oarw/dingzi/internal/proto"
)

func credentialHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// The shared key can enroll a new identity, but cannot replace a bound credential.
func (s *Server) authAgent(r *http.Request) bool {
	id := r.Header.Get(proto.AgentUUIDHeader)
	if _, err := uuid.Parse(id); err != nil {
		return false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var hash string
	var revoked bool
	err = s.store.db.QueryRowContext(ctx, "SELECT token_hash,revoked FROM agent_credentials WHERE uuid=?", id).Scan(&hash, &revoked)
	if err == nil {
		return !revoked && subtle.ConstantTimeCompare([]byte(hash), []byte(credentialHash(token))) == 1
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return s.opts.AgentSecret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(proto.RegistrationHeader)), []byte(s.opts.AgentSecret)) == 1
}

func (s *Store) bindCredential(ctx context.Context, id, token string) error {
	hash := credentialHash(token)
	_, err := s.db.ExecContext(ctx, "INSERT INTO agent_credentials(uuid,token_hash,revoked) VALUES(?,?,0) ON CONFLICT(uuid) DO NOTHING", id, hash)
	if err != nil {
		return err
	}
	var stored string
	var revoked bool
	err = s.db.QueryRowContext(ctx, "SELECT token_hash,revoked FROM agent_credentials WHERE uuid=?", id).Scan(&stored, &revoked)
	if err != nil {
		return err
	}
	if revoked || subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
		return errors.New("机器凭证已吊销或不匹配")
	}
	return nil
}
