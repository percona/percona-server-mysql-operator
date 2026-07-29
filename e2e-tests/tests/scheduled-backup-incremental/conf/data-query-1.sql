USE myDB;

SET SESSION group_concat_max_len = 1048576;

SELECT GROUP_CONCAT(
    CONCAT(
        'SELECT ''', table_name, ''' AS table_name, COUNT(*) AS total_rows FROM `', table_name, '`'
    )
    ORDER BY CAST(SUBSTRING(table_name, 7) AS UNSIGNED)
    SEPARATOR ' UNION ALL '
) INTO @count_query
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name REGEXP '^sbtest[0-9]+$';

SET @count_query = CONCAT(
    'SELECT CONCAT(table_name, '' '', total_rows) AS total_data FROM (',
    @count_query,
    ') AS counts'
);

PREPARE stmt FROM @count_query;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;