CREATE PROCEDURE updateuserstatus_22e2f6ef(IN p_userID BIGINT, IN p_isActive BOOLEAN)
BEGIN
	DECLARE v_newStatus TEXT;
	IF p_isActive THEN
		SET v_newStatus = "ACTIVE";
	ELSE
		SET v_newStatus = "INACTIVE";
	END IF;
	UPDATE users SET status = v_newStatus WHERE id = p_userID;
	SELECT status FROM users WHERE id = p_userID;
END;