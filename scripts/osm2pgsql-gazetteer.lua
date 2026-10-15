-- osm2pgsql flex style: reads an OpenStreetMap extract into a small staging schema (osm_stage).
-- The gazetteer builder (cmd/gazetteer) then turns the staging rows into gaz.features and gaz.names.
-- Usage:
--   osm2pgsql -d "$DATABASE_URL" -O flex -S scripts/osm2pgsql-gazetteer.lua nigeria-latest.osm.pbf
local schema = 'osm_stage'

local roads = osm2pgsql.define_way_table('roads', {
  { column = 'osm_id',    type = 'bigint' },
  { column = 'name',      type = 'text' },
  { column = 'alt_names', type = 'text' },
  { column = 'highway',   type = 'text' },
  { column = 'geom',      type = 'linestring', projection = 4326 },
}, { schema = schema })

local places = osm2pgsql.define_table({
  name = 'places',
  schema = schema,
  ids = { type = 'any', id_column = 'osm_id', type_column = 'osm_type' },
  columns = {
    { column = 'name',      type = 'text' },
    { column = 'alt_names', type = 'text' },
    { column = 'category',  type = 'text' }, -- bank, school, hospital, market, ...
    { column = 'place',     type = 'text' }, -- suburb, town, neighbourhood, ... when it is a locality
    { column = 'geom',      type = 'geometry', projection = 4326 },
  },
})

local addresses = osm2pgsql.define_table({
  name = 'addresses',
  schema = schema,
  ids = { type = 'any', id_column = 'osm_id', type_column = 'osm_type' },
  columns = {
    { column = 'housenumber', type = 'text' },
    { column = 'street',      type = 'text' },
    { column = 'geom',        type = 'geometry', projection = 4326 },
  },
})

local function alt_names(tags)
  local parts = {}
  for _, k in ipairs({ 'alt_name', 'old_name', 'short_name', 'name:en', 'official_name' }) do
    if tags[k] then parts[#parts + 1] = tags[k] end
  end
  if #parts == 0 then return nil end
  return table.concat(parts, ';')
end

local road_types = {
  motorway = true,
  trunk = true,
  primary = true,
  secondary = true,
  tertiary = true,
  unclassified = true,
  residential = true,
  living_street = true,
  service = true,
  motorway_link = true,
  trunk_link = true,
  primary_link = true,
  secondary_link = true,
  tertiary_link = true,
}

-- the kinds of named things people use as landmarks
local function category(tags)
  for _, k in ipairs({ 'amenity', 'shop', 'tourism', 'leisure', 'office', 'building', 'man_made', 'public_transport', 'railway' }) do
    local v = tags[k]
    if v and v ~= 'yes' and v ~= 'no' then return v end
  end
  return nil
end

local function handle(object, geom_fn)
  local tags = object.tags
  if tags.name then
    local cat = category(tags)
    local pl = tags.place
    if cat or pl then
      places:insert({ name = tags.name, alt_names = alt_names(tags), category = cat, place = pl, geom = geom_fn(object) })
    end
  end
  if tags['addr:housenumber'] then
    addresses:insert({ housenumber = tags['addr:housenumber'], street = tags['addr:street'], geom = geom_fn(object) })
  end
end

function osm2pgsql.process_node(object)
  handle(object, function(o) return o:as_point() end)
end

function osm2pgsql.process_way(object)
  local tags = object.tags
  if tags.highway and tags.name and road_types[tags.highway] then
    roads:insert({
      osm_id = object.id,
      name = tags.name,
      alt_names = alt_names(tags),
      highway = tags.highway,
      geom =
          object:as_linestring()
    })
  end
  if object.is_closed then
    handle(object, function(o) return o:as_polygon() end)
  end
end

function osm2pgsql.process_relation(object)
  if object.tags.type == 'multipolygon' then
    handle(object, function(o) return o:as_multipolygon() end)
  end
end
