CREATE OR REPLACE FUNCTION
transferfunds_c4ad3a52(p_fromID BIGINT, p_toID BIGINT, p_amount NUMERIC)
RETURNS TABLE(result BOOLEAN)
LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
v_success BOOLEAN;
BEGIN
	IF p_amount <= 0 THEN
		RAISE EXCEPTION 'invalid transfer amount';
	END IF;
	IF p_fromID = p_toID THEN
		RAISE EXCEPTION 'cannot transfer to self';
	END IF;
	UPDATE accounts SET balance = balance - p_amount WHERE id = p_fromID;
	UPDATE accounts SET balance = balance + p_amount WHERE id = p_toID;
	v_success := TRUE;
	RETURN QUERY SELECT v_success AS result;
END;
$$;