-- reference data shared by every schema

-- The precision classes are the API contract. Radii come from the requirements
-- document; NULL means "the extent of the named area itself".
CREATE TYPE ref.precision AS ENUM ('building', 'street', 'locality', 'lga', 'state', 'none');

CREATE TABLE ref.precision_classes (
    precision ref.precision PRIMARY KEY,
    rank      smallint NOT NULL UNIQUE,            -- 1 = finest
    radius_m  integer CHECK (radius_m > 0)
);
INSERT INTO ref.precision_classes (precision, rank, radius_m) VALUES
    ('building', 1, 50),
    ('street',   2, 300),
    ('locality', 3, 2000),
    ('lga',      4, NULL),
    ('state',    5, NULL),
    ('none',     6, NULL);

-- The 36 states and the Federal Capital Territory. Aliases are normalised
-- (lowercase) spellings people actually write.
CREATE TABLE ref.states (
    id        smallint PRIMARY KEY,
    name      text NOT NULL UNIQUE,
    name_norm text NOT NULL UNIQUE,
    aliases   text[] NOT NULL DEFAULT '{}',
    CHECK (name_norm = lower(name_norm))
);
INSERT INTO ref.states (id, name, name_norm, aliases) VALUES
    (1,  'Abia', 'abia', '{}'),
    (2,  'Adamawa', 'adamawa', '{}'),
    (3,  'Akwa Ibom', 'akwa ibom', '{akwa-ibom}'),
    (4,  'Anambra', 'anambra', '{}'),
    (5,  'Bauchi', 'bauchi', '{}'),
    (6,  'Bayelsa', 'bayelsa', '{}'),
    (7,  'Benue', 'benue', '{}'),
    (8,  'Borno', 'borno', '{}'),
    (9,  'Cross River', 'cross river', '{cross-river}'),
    (10, 'Delta', 'delta', '{}'),
    (11, 'Ebonyi', 'ebonyi', '{}'),
    (12, 'Edo', 'edo', '{}'),
    (13, 'Ekiti', 'ekiti', '{}'),
    (14, 'Enugu', 'enugu', '{}'),
    (15, 'Federal Capital Territory', 'federal capital territory', '{fct,abuja,"fct abuja"}'),
    (16, 'Gombe', 'gombe', '{}'),
    (17, 'Imo', 'imo', '{}'),
    (18, 'Jigawa', 'jigawa', '{}'),
    (19, 'Kaduna', 'kaduna', '{}'),
    (20, 'Kano', 'kano', '{}'),
    (21, 'Katsina', 'katsina', '{}'),
    (22, 'Kebbi', 'kebbi', '{}'),
    (23, 'Kogi', 'kogi', '{}'),
    (24, 'Kwara', 'kwara', '{}'),
    (25, 'Lagos', 'lagos', '{}'),
    (26, 'Nasarawa', 'nasarawa', '{nassarawa}'),
    (27, 'Niger', 'niger', '{}'),
    (28, 'Ogun', 'ogun', '{}'),
    (29, 'Ondo', 'ondo', '{}'),
    (30, 'Osun', 'osun', '{}'),
    (31, 'Oyo', 'oyo', '{}'),
    (32, 'Plateau', 'plateau', '{}'),
    (33, 'Rivers', 'rivers', '{}'),
    (34, 'Sokoto', 'sokoto', '{}'),
    (35, 'Taraba', 'taraba', '{}'),
    (36, 'Yobe', 'yobe', '{}'),
    (37, 'Zamfara', 'zamfara', '{}');

-- The normalizer's dictionary lives in data, so it can be corrected without a release.
--   abbreviation  surface form  -> canonical word        (opp. -> opposite)
--   relation      surface form  -> canonical relation    (next to -> beside)
--   feature_word  surface form  -> feature category      (mosque -> worship)
CREATE TYPE ref.term_kind AS ENUM ('abbreviation', 'relation', 'feature_word');

CREATE TABLE ref.address_terms (
    term      text NOT NULL,
    kind      ref.term_kind NOT NULL,
    expansion text NOT NULL,
    PRIMARY KEY (term, kind),
    CHECK (term = lower(term) AND expansion = lower(expansion))
);
INSERT INTO ref.address_terms (term, kind, expansion) VALUES
    ('opp',    'abbreviation', 'opposite'),
    ('b/stop', 'abbreviation', 'bus stop'),
    ('bstop',  'abbreviation', 'bus stop'),
    ('rd',     'abbreviation', 'road'),
    ('ave',    'abbreviation', 'avenue'),
    ('st',     'abbreviation', 'street'),
    ('cres',   'abbreviation', 'crescent'),
    ('cl',     'abbreviation', 'close'),
    ('dr',     'abbreviation', 'drive'),
    ('ln',     'abbreviation', 'lane'),
    ('est',    'abbreviation', 'estate'),
    ('jct',    'abbreviation', 'junction'),
    ('nr',     'abbreviation', 'near'),
    ('hse',    'abbreviation', 'house'),
    ('bldg',   'abbreviation', 'building'),
    ('plt',    'abbreviation', 'plot'),
    ('blk',    'abbreviation', 'block'),
    ('opposite',   'relation', 'opposite'),
    ('beside',     'relation', 'beside'),
    ('besides',    'relation', 'beside'),
    ('next to',    'relation', 'beside'),
    ('adjacent to','relation', 'beside'),
    ('behind',     'relation', 'behind'),
    ('after',      'relation', 'after'),
    ('before',     'relation', 'before'),
    ('near',       'relation', 'near'),
    ('close to',   'relation', 'near'),
    ('inside',     'relation', 'inside'),
    ('within',     'relation', 'inside'),
    ('off',        'relation', 'off'),
    ('road',       'feature_word', 'road'),
    ('street',     'feature_word', 'road'),
    ('avenue',     'feature_word', 'road'),
    ('close',      'feature_word', 'road'),
    ('crescent',   'feature_word', 'road'),
    ('drive',      'feature_word', 'road'),
    ('lane',       'feature_word', 'road'),
    ('estate',     'feature_word', 'locality'),
    ('junction',   'feature_word', 'junction'),
    ('roundabout', 'feature_word', 'junction'),
    ('market',     'feature_word', 'market'),
    ('church',     'feature_word', 'worship'),
    ('mosque',     'feature_word', 'worship'),
    ('school',     'feature_word', 'education'),
    ('college',    'feature_word', 'education'),
    ('university', 'feature_word', 'education'),
    ('hospital',   'feature_word', 'health'),
    ('clinic',     'feature_word', 'health'),
    ('bank',       'feature_word', 'bank'),
    ('hotel',      'feature_word', 'lodging'),
    ('filling station', 'feature_word', 'fuel'),
    ('petrol station',  'feature_word', 'fuel'),
    ('bus stop',   'feature_word', 'transport'),
    ('plaza',      'feature_word', 'shopping'),
    ('mall',       'feature_word', 'shopping');
