# Map assets

Bundled with the app (installed next to `PetTracker.exe`), never downloaded at runtime.

- `maps/<zone>/<x>_<y>.webp`: zone map tiles, 512 px, from [wikily.gg](https://wikily.gg/aion-2/interactive-map)'s interactive map at zoom -1 (half of full resolution). Tile `x` runs left→right from 0, `y` runs top→bottom from `-n` to `-1`.
- `spawns/spawns.json.gz`: monster spawn points per zone in map pixels (`frames` = full map size), from wikily.gg's monster data.

Zones: verteron, altgard (16384 px), ishalgen (10240), chaotic-lower-reshanta, poeta (8192).

Map art is NCSOFT's AION 2 game art via wikily.gg. To refresh, re-export the tiles and spawn data and replace these folders.
