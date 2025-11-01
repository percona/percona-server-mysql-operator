USE myDB;

SET SESSION max_allowed_packet = 1073741824;

DROP PROCEDURE IF EXISTS count_rows;

DELIMITER $$

CREATE PROCEDURE count_rows()
BEGIN
    DECLARE done INT DEFAULT FALSE;
    DECLARE tbl_name VARCHAR(255);
    DECLARE cur CURSOR FOR 
        SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE();
    DECLARE CONTINUE HANDLER FOR NOT FOUND SET done = TRUE;

    DROP TEMPORARY TABLE IF EXISTS temp_counts;
    CREATE TEMPORARY TABLE temp_counts (
        table_name VARCHAR(255),
        total_rows INT
    );

    OPEN cur;
    read_loop: LOOP
        FETCH cur INTO tbl_name;
        IF done THEN
            LEAVE read_loop;
        END IF;

        SET @sql = CONCAT(
            'INSERT INTO temp_counts SELECT "', tbl_name, '", COUNT(*) FROM ', tbl_name
        );
        PREPARE stmt FROM @sql;
        EXECUTE stmt;
        DEALLOCATE PREPARE stmt;
    END LOOP;
    CLOSE cur;

    SELECT CONCAT(table_name, ' ', total_rows) AS total_data FROM temp_counts;
END$$

DELIMITER ;

CALL count_rows();
