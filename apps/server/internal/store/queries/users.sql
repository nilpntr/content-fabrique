-- Idempotent for Clerk webhooks: a user keeps its first id, and the email follows Clerk.
-- name: UpsertUserByClerkID :one
INSERT INTO users (id, clerk_user_id, email)
VALUES (@id, @clerk_user_id, @email)
ON CONFLICT (clerk_user_id) DO UPDATE SET email = EXCLUDED.email
RETURNING *;
