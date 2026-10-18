-- A small synthetic Bible database for tests. The verse texts are made up
-- so that tests can check exact output.
--
-- Translations: kjv, nrsvue (the default) and ntonly (no Old Testament).
-- notes and nobookid look like neither translations nor paragraphs and
-- must be ignored.
--
-- Books chosen to exercise resolution: "jud" is ambiguous (Judges, Jude),
-- "john" must pick John over 1 John, and kjv's "Esther" must win over
-- "Esther (Greek)", which it is a prefix of.

CREATE TABLE nrsvue (book_id INTEGER, book TEXT, chapter INTEGER, verse INTEGER, text TEXT);
CREATE INDEX nrsvue_ref ON nrsvue (book_id, chapter, verse);
INSERT INTO nrsvue VALUES
	(1, 'Genesis', 1, 1, 'In the beginning there was faith.'),
	(1, 'Genesis', 1, 2, 'The second verse.'),
	(1, 'Genesis', 1, 3, 'The third verse.'),
	(1, 'Genesis', 1, 4, 'Faith begins the fourth verse.'),
	(1, 'Genesis', 1, 5, 'The fifth verse.'),
	(1, 'Genesis', 2, 1, 'Chapter two begins.'),
	(1, 'Genesis', 2, 2, 'Chapter two continues.'),
	(7, 'Judges', 1, 1, 'The judges verse.'),
	(32, 'Jonah', 1, 1, 'The Jonah verse.'),
	(43, 'John', 3, 1, 'John three one.'),
	(43, 'John', 3, 2, 'John three two, with a.b inside.'),
	(43, 'John', 3, 3, 'John three three, with axb inside.'),
	(43, 'John', 3, 16, 'For faith so loved the world.'),
	(45, 'Romans', 1, 1, 'Romans one one, by faith.'),
	(62, '1 John', 1, 1, 'First John one one.'),
	(65, 'Jude', 1, 1, 'Jude one one.');

CREATE TABLE kjv (book_id INTEGER, book TEXT, chapter INTEGER, verse INTEGER, text TEXT);
CREATE INDEX kjv_ref ON kjv (book_id, chapter, verse);
INSERT INTO kjv VALUES
	(1, 'Genesis', 1, 1, 'KJV beginning with faith.'),
	(1, 'Genesis', 1, 2, 'KJV second.'),
	(1, 'Genesis', 1, 4, 'KJV fourth; the third is omitted.'),
	(17, 'Esther', 1, 1, 'Esther one one.'),
	(18, 'Esther (Greek)', 1, 1, 'Greek Esther one one.'),
	(43, 'John', 3, 16, 'KJV for faith so loved.');

CREATE TABLE ntonly (book_id INTEGER, book TEXT, chapter INTEGER, verse INTEGER, text TEXT);
CREATE INDEX ntonly_ref ON ntonly (book_id, chapter, verse);
INSERT INTO ntonly VALUES
	(43, 'John', 3, 16, 'NT John three sixteen.'),
	(45, 'Romans', 1, 1, 'NT Romans one one.');

CREATE TABLE notes (id INTEGER, body TEXT);
INSERT INTO notes VALUES (1, 'Not a translation.');

-- Has every translation column except book_id.
CREATE TABLE nobookid (book TEXT, chapter INTEGER, verse INTEGER, text TEXT);
INSERT INTO nobookid VALUES ('Genesis', 1, 1, 'No book_id column.');

-- Same schema as bible.ParagraphSchema. Book names match
-- case-insensitively, and starts need not be sorted.
CREATE TABLE paragraphs (
	book TEXT NOT NULL COLLATE NOCASE,
	chapter INTEGER NOT NULL,
	starts TEXT NOT NULL,
	PRIMARY KEY (book, chapter)) WITHOUT ROWID;
INSERT INTO paragraphs VALUES
	('genesis', 1, '3 1'),
	('John', 3, '1 16');
