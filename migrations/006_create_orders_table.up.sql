-- Table: public.orders

CREATE TABLE IF NOT EXISTS public.orders
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id uuid NOT NULL,
    payment_mode text NOT NULL,
    mollie_payment_id text,
    mollie_payment_url text,
    status text NOT NULL DEFAULT 'PENDING'::text,
    CONSTRAINT orders_pkey PRIMARY KEY (id),
    CONSTRAINT orders_user_id_foreign FOREIGN KEY (user_id)
        REFERENCES public.users (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT orders_payment_mode_check CHECK (payment_mode::text = ANY (ARRAY['CASH'::text, 'ONLINE'::text, 'TERMINAL'::text]::text[])),
    CONSTRAINT orders_status_check CHECK (status::text = ANY (ARRAY['PENDING'::text, 'CONFIRMED'::text, 'PREPARING'::text, 'AWAITING_PICK_UP'::text, 'PICKED_UP'::text, 'OUT_FOR_DELIVERY'::text, 'DELIVERED'::text, 'CANCELLED'::text, 'FAILED'::text]::text[]))
);
