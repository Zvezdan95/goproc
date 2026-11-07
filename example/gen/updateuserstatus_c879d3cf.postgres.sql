CREATE OR REPLACE FUNCTION
updateuserstatus_c879d3cf(p_userID BIGINT, p_isActive BOOLEAN)
RETURNS TABLE(status TEXT)
LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
v_newStatus TEXT;
BEGIN
	IF p_isActive THEN
		v_newStatus := 'ACTIVE';
	ELSE
		v_newStatus := 'INACTIVE';
	END IF;
	UPDATE users SET status = v_newStatus WHERE id = p_userID;
	RETURN QUERY SELECT status FROM users WHERE id = p_userID;
END;
$$;