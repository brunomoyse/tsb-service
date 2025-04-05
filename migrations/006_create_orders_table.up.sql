CREATE TABLE IF NOT EXISTS public.orders
(
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamp(0) with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp(0) with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id uuid NOT NULL,
    order_status text NOT NULL DEFAULT 'PENDING'::text, -- 'PENDING', 'CONFIRMED', etc.
    order_type text NOT NULL,                           -- 'DELIVERY', 'PICKUP'
    is_online_payment boolean NOT NULL DEFAULT false,
    discount_amount numeric(10,2),
    delivery_fee numeric(10,2),                     -- if order_type is DELIVERY
    total_price numeric(10,2) NOT NULL,
    estimated_ready_time timestamp(0) with time zone, -- when order is ready or delivered
    address_id uuid,                                   -- if order_type is DELIVERY
    address_extra text,                                -- extra info about the address
    order_note text,                                -- general comments about the order
    order_extra jsonb,                                 -- extra info about the order
    CONSTRAINT orders_pkey PRIMARY KEY (id),
    CONSTRAINT orders_user_id_foreign FOREIGN KEY (user_id)
        REFERENCES public.users (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT orders_status_check CHECK (
        order_status = ANY (
            ARRAY[
                'PENDING', 'CONFIRMED', 'PREPARING', 'AWAITING_PICK_UP',
                'PICKED_UP', 'OUT_FOR_DELIVERY', 'DELIVERED', 'CANCELLED', 'FAILED'
                ]
            )
        ),
    CONSTRAINT orders_type_check CHECK (
        order_type = ANY (ARRAY['DELIVERY', 'PICKUP'])
        )
);

ALTER TABLE orders
    ADD CONSTRAINT fk_orders_user
        FOREIGN KEY (user_id)
            REFERENCES users(id)
            ON DELETE RESTRICT;
