CREATE PROCEDURE getoptionaldata_14c6babd(IN p_userID BIGINT, IN p_defaultNote TEXT)
BEGIN
	DECLARE v_Notes VARCHAR(255);
	IF p_defaultNote IS NULL THEN
		SELECT notes FROM user_data WHERE id = p_userID;
	END IF;
	SET v_Notes = p_defaultNote;
END;