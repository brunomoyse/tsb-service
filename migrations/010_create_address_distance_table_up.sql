CREATE TABLE IF NOT EXISTS public.address_distance
(
    address_id text COLLATE pg_catalog."default" NOT NULL,
    distance double precision NOT NULL,
    CONSTRAINT address_distance_pkey PRIMARY KEY (address_id)
)
