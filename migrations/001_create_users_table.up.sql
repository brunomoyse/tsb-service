-- Table: public.users

CREATE TABLE IF NOT EXISTS public.users
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone,
    name TEXT NOT NULL,
    email TEXT NOT NULL,
    email_verified_at timestamp(0) without time zone,
    password_hash TEXT,
    salt TEXT,
    remember_token TEXT,
    google_id TEXT,

    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT users_email_unique UNIQUE (email)
);
