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
