# Recommender

Recommender that uses a mixture of data from my watch history, my ratings, what is in my Plex library, what is on Anilist to recommend me stuff to watch.

The app is built using the following technologies:

 - Go
 - Chi
 - Gorm
 - SQLite

The homepage recommends me the following: 

 - Four movies:
   - One I haven't seen that is funny
   - One I haven't seen that is action or a drama
   - One I have seen before
 - Three anime I haven't seen
 - Three TV show I haven't seen

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

## Future Data Sources

 - Goodreads
 - My Kindle Library
 - My Spotify Library
 - My Kavita Library

## Running the Service

### Prerequisites

- Docker and Docker Compose installed
- Plex server URL and token
- OpenAI API key

### Environment Variables

Create a `.env` file in the project root with the following variables:

```env
PLEX_URL=your_plex_server_url
PLEX_TOKEN=your_plex_token
OPENAI_API_KEY=your_openai_api_key
```

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
