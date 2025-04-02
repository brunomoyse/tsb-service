CREATE TABLE IF NOT EXISTS public.mollie_payments
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    resource text,
    mollie_payment_id varchar(255) NOT NULL,  -- corresponds to Payment.ID
    status text NOT NULL,
    description text,
    cancel_url text,
    webhook_url text,
    country_code text,
    restrict_payment_methods_to_country text,
    profile_id text,
    settlement_id text,
    order_id uuid NOT NULL,                   -- reference to the related order
    is_cancelable boolean NOT NULL DEFAULT false,
    mode text,
    locale text,
    method text,
    metadata jsonb,
    links jsonb,
    created_at timestamp(0) with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    authorized_at timestamp(0) with time zone,
    paid_at timestamp(0) with time zone,
    canceled_at timestamp(0) with time zone,
    expires_at timestamp(0) with time zone,
    expired_at timestamp(0) with time zone,
    failed_at timestamp(0) with time zone,
    amount numeric(10,2),
    amount_refunded numeric(10,2),
    amount_remaining numeric(10,2),
    amount_captured numeric(10,2),
    amount_charged_back numeric(10,2),
    settlement_amount numeric(10,2),
    CONSTRAINT mollie_payments_pkey PRIMARY KEY (id),
    CONSTRAINT mollie_payments_payment_id_unique UNIQUE (mollie_payment_id),
    CONSTRAINT mollie_payments_order_id_foreign FOREIGN KEY (order_id)
        REFERENCES public.orders (id) ON DELETE CASCADE
);
