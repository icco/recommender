# Recommender

Recommender that uses a mixture of data from my watch history, my ratings, what is in my Plex library, what is on Anilist to recommend me stuff to watch.

The app is built using the following technologies:

 - Go
 - Chi v5 for routing
 - Gorm for ORM
 - SQLite for storage
 - log/slog for all logs in json

The homepage recommends me the following: 

 - Four movies:
   - One I haven't seen that is funny
   - One I haven't seen that is action or a drama
   - One I have seen before
 - Three TV shows I haven't seen

it displays the following:

 - The movie poster
 - The title
 - The year
 - The rating
 - The genre
 - The runtime

It generates a new recommendation every day. It stores the recommendations in a SQLite database. You can view past recomendations by going to other date pages.

## Data Sources

 - My Plex library
 - My Anilist library
 - My Letterboxd ratings
 - My Traktv watch history

### Future Data Sources

 - Goodreads
 - My Kindle Library
 - My Spotify Library
 - My Kavita Library

## API Endpoints

 - `GET /` - Homepage
 - `GET /cron/recommend` - Generate new recommendations
 - `GET /cron/cache` - Update the cache of Plex and Anilist
 - `GET /dates` - List of all dates with recommendations
 - `GET /date/2025-05-19` - Recommendations for a specific date
 - `GET /stats` - View statistics about the recommendations database

## Repository Structure

```
recommender/
├── handlers/           # HTTP request handlers and templates
├── lib/               # Core libraries and business logic
│   ├── db/           # Database utilities
│   ├── plex/         # Plex API client
│   └── recommender/  # Recommendation generation logic
├── models/           # Data models and database schemas
└── data/            # Persistent data storage
```

## Recommendation Logic

This uses OpenAI to generate personalized recommendations based on your watch history, ratings, and preferences. The cron is run once an hour, and checks to make sure there are the correct number of things recommended. If there are not, it requests OpenAI for recommendations in JSON of the things it is missing. I really like Anime, which is a genre of TV Show, so I usually have OpenAI prefer anime in its recommendations of TV shows.

## Running the Service

### Running with Docker Compose

1. Build and start the service:
```bash
docker compose up -d
```

2. The service will be available at `http://localhost:8080`

3. To generate new recommendations, visit `http://localhost:8080/cron`

4. To view logs:
```bash
docker compose logs -f
```

5. To stop the service:
```bash
docker compose down
```

The SQLite database will be persisted in the `./data` directory.
