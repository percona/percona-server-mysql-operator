USE pvctest;
DROP PROCEDURE IF EXISTS insert_pvc_test_data;

DELIMITER //

CREATE PROCEDURE insert_pvc_test_data()
BEGIN
  DECLARE i INT DEFAULT 1;
  WHILE i <= 50 DO
    INSERT INTO data (id, data)
    VALUES (i, CONCAT('test data row ', i, ' with some content to use storage space'));
    SET i = i + 1;
  END WHILE;
END;
//

DELIMITER ;

CALL insert_pvc_test_data();
DROP PROCEDURE insert_pvc_test_data;
