CREATE OR REPLACE PROCEDURE
getoptionaldata_4b5adaf5(p_userID BIGINT, p_defaultNote VARCHAR(255))
LANGUAGE plpgsql AS $$
#variable_conflict use_variable
BEGIN
	IF p_defaultNote IS NULL THEN
		SELECT notes FROM user_data WHERE id = p_userID;
	END IF;
	v_Notes := p_defaultNote;
END;
$$;