CREATE OR REPLACE PROCEDURE
updateuserstatus_22e2f6ef(p_userID BIGINT, p_isActive BOOLEAN)
LANGUAGE plpgsql AS $$
#variable_conflict use_variable
DECLARE
v_newStatus TEXT;
BEGIN
	IF p_isActive THEN
		v_newStatus := 'ACTIVE';
	ELSE
		v_newStatus := 'INACTIVE';
	END IF;
	UPDATE users SET status = v_newStatus WHERE id = p_userID;
	SELECT status FROM users WHERE id = p_userID;
END;
$$;