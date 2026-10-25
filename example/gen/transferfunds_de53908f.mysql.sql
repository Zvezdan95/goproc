CREATE PROCEDURE transferfunds_de53908f(IN p_fromID BIGINT, IN p_toID BIGINT, IN p_amount DOUBLE)
BEGIN
	DECLARE v_success BOOLEAN;
	IF p_amount <= 0 THEN
		SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = "invalid transfer amount";
	END IF;
	IF p_fromID = p_toID THEN
		SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = "cannot transfer to self";
	END IF;
	UPDATE accounts SET balance = balance - p_amount WHERE id = p_fromID;
	UPDATE accounts SET balance = balance + p_amount WHERE id = p_toID;
	SET v_success = 1;
END;