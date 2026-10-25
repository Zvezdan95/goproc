CREATE OR REPLACE PROCEDURE
transferfunds_de53908f(p_fromID BIGINT, p_toID BIGINT, p_amount NUMERIC)
LANGUAGE plpgsql AS $$
#variable_conflict use_variable
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
END;
$$;