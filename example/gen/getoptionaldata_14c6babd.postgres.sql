CREATE OR REPLACE FUNCTION
getoptionaldata_14c6babd(p_userID BIGINT, p_defaultNote TEXT)
RETURNS TABLE(Notes TEXT)
LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
v_Notes VARCHAR;
BEGIN
	IF p_defaultNote IS NULL THEN
		RETURN QUERY SELECT notes FROM user_data WHERE id = p_userID;
	END IF;
	v_Notes := p_defaultNote;
END;
$$;