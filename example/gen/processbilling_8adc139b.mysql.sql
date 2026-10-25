CREATE PROCEDURE processbilling_8adc139b(IN p_userID BIGINT, IN p_amount DOUBLE)
BEGIN
	IF p_amount <= 0 THEN
		SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = "amount must be greater than zero";
	END IF;
	SELECT id, total FROM invoices WHERE user_id = p_userID;
END;