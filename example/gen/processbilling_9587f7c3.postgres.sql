CREATE OR REPLACE FUNCTION
processbilling_9587f7c3(p_userID BIGINT, p_amount NUMERIC)
RETURNS TABLE(id BIGINT, total NUMERIC)
LANGUAGE plpgsql AS $$
#variable_conflict use_column
BEGIN
	IF p_amount <= 0 THEN
		RAISE EXCEPTION 'amount must be greater than zero';
	END IF;
	RETURN QUERY SELECT id, total FROM invoices WHERE user_id = p_userID;
END;
$$;