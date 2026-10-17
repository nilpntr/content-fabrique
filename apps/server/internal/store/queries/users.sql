-- Idempotent for Clerk webhooks: a user keeps its first id (the uuidv7() default), and the email follows Clerk.
-- name: UpsertUserByClerkID :one
INSERT INTO users (clerk_user_id, email)
VALUES (@clerk_user_id, @email)
ON CONFLICT (clerk_user_id) DO UPDATE SET email = EXCLUDED.email
RETURNING *;
