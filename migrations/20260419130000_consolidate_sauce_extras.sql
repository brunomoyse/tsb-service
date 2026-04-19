-- +goose Up
-- +goose StatementBegin
-- Consolidate legacy {"name":"sauces","options":[<sauce1>,<sauce2>]} entries
-- into a single {"name":"sauce","options":["sweet"|"salty"|"both"]}. If neither
-- sweet nor salty was chosen, the entry is removed (absence = none).
DO $$
DECLARE
    r RECORD;
    new_extras jsonb;
    sauces_entry jsonb;
    opts jsonb;
    has_sweet boolean;
    has_salty boolean;
    sauce_val text;
BEGIN
    FOR r IN
        SELECT id, order_extra FROM orders
        WHERE jsonb_typeof(order_extra) = 'array'
          AND order_extra @> '[{"name":"sauces"}]'::jsonb
    LOOP
        SELECT elem INTO sauces_entry
        FROM jsonb_array_elements(r.order_extra) AS elem
        WHERE elem->>'name' = 'sauces'
        LIMIT 1;

        opts := sauces_entry->'options';
        has_sweet := COALESCE(opts @> '"sweet"'::jsonb, false);
        has_salty := COALESCE(opts @> '"salty"'::jsonb, false);

        SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb) INTO new_extras
        FROM jsonb_array_elements(r.order_extra) AS elem
        WHERE elem->>'name' IS DISTINCT FROM 'sauces';

        IF has_sweet AND has_salty THEN
            sauce_val := 'both';
        ELSIF has_sweet THEN
            sauce_val := 'sweet';
        ELSIF has_salty THEN
            sauce_val := 'salty';
        ELSE
            sauce_val := NULL;
        END IF;

        IF sauce_val IS NOT NULL THEN
            new_extras := new_extras || jsonb_build_array(
                jsonb_build_object('name', 'sauce', 'options', jsonb_build_array(sauce_val))
            );
        END IF;

        UPDATE orders SET order_extra = new_extras WHERE id = r.id;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Best-effort reverse: rebuild {"name":"sauces","options":[a,b]}.
-- "both" splits back into ["sweet","salty"]; single values double up.
DO $$
DECLARE
    r RECORD;
    new_extras jsonb;
    sauce_entry jsonb;
    sauce_val text;
    options_arr jsonb;
BEGIN
    FOR r IN
        SELECT id, order_extra FROM orders
        WHERE jsonb_typeof(order_extra) = 'array'
          AND order_extra @> '[{"name":"sauce"}]'::jsonb
    LOOP
        SELECT elem INTO sauce_entry
        FROM jsonb_array_elements(r.order_extra) AS elem
        WHERE elem->>'name' = 'sauce'
        LIMIT 1;

        sauce_val := sauce_entry->'options'->>0;

        CASE sauce_val
            WHEN 'both' THEN options_arr := '["sweet","salty"]'::jsonb;
            WHEN 'sweet' THEN options_arr := '["sweet","sweet"]'::jsonb;
            WHEN 'salty' THEN options_arr := '["salty","salty"]'::jsonb;
            ELSE options_arr := '["none","none"]'::jsonb;
        END CASE;

        SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb) INTO new_extras
        FROM jsonb_array_elements(r.order_extra) AS elem
        WHERE elem->>'name' IS DISTINCT FROM 'sauce';

        new_extras := new_extras || jsonb_build_array(
            jsonb_build_object('name', 'sauces', 'options', options_arr)
        );

        UPDATE orders SET order_extra = new_extras WHERE id = r.id;
    END LOOP;
END $$;
-- +goose StatementEnd
