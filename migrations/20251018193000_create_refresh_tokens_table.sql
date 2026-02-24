-- +goose Up
-- Table: public.refresh_tokens

CREATE TABLE IF NOT EXISTS public.refresh_tokens
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    token_hash TEXT NOT NULL,
    created_at timestamp(0) with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp(0) with time zone NOT NULL,
    revoked_at timestamp(0) with time zone,
    CONSTRAINT refresh_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT refresh_tokens_token_hash_unique UNIQUE (token_hash),
    CONSTRAINT refresh_tokens_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES public.users (id) ON DELETE CASCADE
);

CREATE INDEX idx_refresh_tokens_user_id ON public.refresh_tokens(user_id);
CREATE INDEX idx_refresh_tokens_token_hash ON public.refresh_tokens(token_hash);
CREATE INDEX idx_refresh_tokens_expires_at ON public.refresh_tokens(expires_at);

-- +goose Down
DROP TABLE IF EXISTS public.refresh_tokens;
