CREATE PROCEDURE getoptionaldata_4b21c7ef(IN p_userID BIGINT, IN p_defaultNote VARCHAR(255))
BEGIN
	DECLARE v_Notes TEXT;
	IF p_defaultNote IS NULL THEN
		SELECT notes FROM user_data WHERE id = p_userID;
	END IF;
	SET v_Notes = p_defaultNote;
END;