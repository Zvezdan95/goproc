CREATE OR REPLACE PROCEDURE
processbilling_8adc139b(p_userID BIGINT, p_amount NUMERIC)
LANGUAGE plpgsql AS $$
#variable_conflict use_variable
BEGIN
	IF p_amount <= 0 THEN
		RAISE EXCEPTION 'amount must be greater than zero';
	END IF;
	SELECT id, total FROM invoices WHERE user_id = p_userID;
END;
$$;