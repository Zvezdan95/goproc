CREATE PROCEDURE complexformattest_a1e80486(IN p_action BIGINT, IN p_data VARCHAR(255))
BEGIN
	DECLARE v_status TEXT;
	DECLARE v_i BIGINT;
	IF p_action = 1 THEN
		SET v_status = "START";
	ELSE
		SET v_status = "STOP";
	END IF;
	SET v_i = 0;
	WHILE v_i < 5 DO
		IF v_status = "START" THEN
			CASE p_action
			WHEN 1 THEN
				SET v_status = "ONGOING";
			WHEN 2 THEN
				SET v_status = "PAUSED";
			END CASE;
		END IF;
		SET v_i = v_i + 1;
	END WHILE;
END;