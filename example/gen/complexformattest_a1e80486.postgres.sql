CREATE OR REPLACE PROCEDURE
complexformattest_a1e80486(p_action BIGINT, p_data VARCHAR(255))
LANGUAGE plpgsql AS $$
#variable_conflict use_variable
DECLARE
v_status TEXT;
v_i BIGINT;
BEGIN
	IF p_action = 1 THEN
		v_status := 'START';
	ELSE
		v_status := 'STOP';
	END IF;
	v_i := 0;
	WHILE v_i < 5 LOOP
		IF v_status = 'START' THEN
			CASE p_action
			WHEN 1 THEN
				v_status := 'ONGOING';
			WHEN 2 THEN
				v_status := 'PAUSED';
			END CASE;
		END IF;
		v_i := v_i + 1;
	END LOOP;
END;
$$;