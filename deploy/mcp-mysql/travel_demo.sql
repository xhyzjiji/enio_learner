-- 演示库：团队出行计划，供 MCP MySQL demo 使用。
-- Demo database: team trip plans, used by the MCP MySQL demo.
--
-- 用法 / Usage: mysql -uroot < deploy/mcp-mysql/travel_demo.sql

-- 必须显式声明：Homebrew 的 mysql CLI 默认 character_set_client=latin1，
-- 会把中文双重编码写进表里，CLI 自己读回来看着正常，但 mysql2 客户端读到的是乱码。
-- Required: Homebrew's mysql CLI defaults to character_set_client=latin1, which
-- double-encodes non-ASCII on insert. The CLI reads it back fine, but the mysql2 client sees mojibake.
SET NAMES utf8mb4;

CREATE DATABASE IF NOT EXISTS travel_demo DEFAULT CHARSET utf8mb4;
USE travel_demo;

DROP TABLE IF EXISTS trip_plans;
DROP TABLE IF EXISTS employees;

CREATE TABLE employees (
  id         INT PRIMARY KEY,
  name       VARCHAR(32) NOT NULL,
  department VARCHAR(32) NOT NULL
);

CREATE TABLE trip_plans (
  id          INT PRIMARY KEY,
  employee_id INT         NOT NULL,
  city        VARCHAR(32) NOT NULL,
  depart_date DATE        NOT NULL,
  status      VARCHAR(16) NOT NULL  -- confirmed / pending
);

INSERT INTO employees (id, name, department) VALUES
  (1,  '张三', '客户端'),
  (2,  '李四', '客户端'),
  (3,  '王五', '服务端'),
  (4,  '赵六', '服务端'),
  (5,  '孙七', '测试'),
  (6,  '周八', '客户端'),
  (7,  '吴九', '服务端'),
  (8,  '郑十', '测试'),
  (9,  '冯一', '客户端'),
  (10, '陈二', '服务端');

-- 日期一律相对 CURDATE() 生成，保证任何时候跑 demo 都有“本周”数据。
-- All dates are relative to CURDATE() so the demo always has data for "this week".
INSERT INTO trip_plans (id, employee_id, city, depart_date, status) VALUES
  -- 未来 7 天内已确认：珠海 5 / 江门 2 / 广州 1
  -- Confirmed within the next 7 days: Zhuhai 5 / Jiangmen 2 / Guangzhou 1
  (1,  1,  '珠海', DATE_ADD(CURDATE(), INTERVAL 1 DAY),  'confirmed'),
  (2,  2,  '珠海', DATE_ADD(CURDATE(), INTERVAL 1 DAY),  'confirmed'),
  (3,  3,  '珠海', DATE_ADD(CURDATE(), INTERVAL 2 DAY),  'confirmed'),
  (4,  6,  '珠海', DATE_ADD(CURDATE(), INTERVAL 3 DAY),  'confirmed'),
  (5,  9,  '珠海', DATE_ADD(CURDATE(), INTERVAL 5 DAY),  'confirmed'),
  (6,  4,  '江门', DATE_ADD(CURDATE(), INTERVAL 2 DAY),  'confirmed'),
  (7,  7,  '江门', DATE_ADD(CURDATE(), INTERVAL 4 DAY),  'confirmed'),
  (8,  5,  '广州', DATE_ADD(CURDATE(), INTERVAL 3 DAY),  'confirmed'),
  -- 未来 7 天内待确认：检验模型有没有按 status 过滤
  -- Pending within the next 7 days: checks whether the model filters on status
  (9,  8,  '珠海', DATE_ADD(CURDATE(), INTERVAL 6 DAY),  'pending'),
  (10, 10, '江门', DATE_ADD(CURDATE(), INTERVAL 6 DAY),  'pending'),
  -- 三周后出发：检验模型有没有按日期过滤
  -- Departing three weeks out: checks whether the model filters on date
  (11, 1,  '广州', DATE_ADD(CURDATE(), INTERVAL 18 DAY), 'confirmed'),
  (12, 3,  '广州', DATE_ADD(CURDATE(), INTERVAL 20 DAY), 'confirmed'),
  (13, 5,  '广州', DATE_ADD(CURDATE(), INTERVAL 21 DAY), 'confirmed'),
  (14, 7,  '深圳', DATE_ADD(CURDATE(), INTERVAL 22 DAY), 'confirmed'),
  (15, 9,  '深圳', DATE_ADD(CURDATE(), INTERVAL 25 DAY), 'confirmed');
