-- json_each(text) is a compatibility shim so that SQLite-style
-- json_each(json_array_string) queries work unchanged on PostgreSQL.
-- SQLite's built-in json_each accepts a text JSON array and exposes a
-- "value" column; this overload makes PostgreSQL behave the same way.
-- The existing json_each(json) built-in (for objects) is unaffected —
-- PostgreSQL resolves the correct overload by argument type.
CREATE OR REPLACE FUNCTION json_each(input text)
RETURNS TABLE(value text)
LANGUAGE sql AS $$
    SELECT * FROM json_array_elements_text(input::json)
$$;
