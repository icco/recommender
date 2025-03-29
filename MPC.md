# Media Project Controller (MPC) Decisions and Advice

## Project Structure

1. **Package Organization**
   - `lib/`: Core business logic and external service integrations
   - `models/`: Data models and database schemas
   - `handlers/`: HTTP request handlers
   - `templates/`: HTML templates for the UI

2. **Database Design**
   - Using SQLite with GORM for simplicity and portability
   - Separate models for recommendations and content (movies, anime, TV shows)
   - Using many-to-many relationships for recommendations and content
   - Caching Plex data to reduce API calls

3. **API Integration**
   - Plex integration using the official Plex API
   - OpenAI integration for recommendation generation
   - Future integrations planned for Anilist, Letterboxd, and Traktv

4. **Recommendation Logic**
   - Using OpenAI to generate recommendations based on:
     - Unwatched content from Plex
     - User ratings and watch history
     - Content metadata (ratings, genres, etc.)
   - Recommendations are generated daily and stored in the database

5. **UI Design**
   - Clean, modern interface using Tailwind CSS
   - Responsive design for mobile and desktop
   - Clear display of content metadata
   - Easy navigation between dates

## Build Issues and Solutions

1. **Missing Templates**
   - Create templates directory with required HTML files
   - Use Go's template system with proper escaping
   - Implement responsive design using Tailwind CSS

2. **Database Access**
   - Move database operations to lib/db package
   - Implement proper error handling and logging
   - Use transactions for data consistency

3. **API Integration**
   - Implement proper error handling for external APIs
   - Add retry logic for failed requests
   - Cache responses to reduce API calls

4. **Code Organization**
   - Keep business logic in lib packages
   - Use interfaces for better testability
   - Implement proper dependency injection

## Future Improvements

1. **Data Sources**
   - Add support for Anilist API
   - Integrate Letterboxd ratings
   - Add Traktv watch history
   - Consider Goodreads, Kindle, Spotify, and Kavita

2. **Recommendation Engine**
   - Improve recommendation quality
   - Add user preferences
   - Consider collaborative filtering
   - Add content-based filtering

3. **Performance**
   - Implement caching for external API calls
   - Optimize database queries
   - Add background job processing
   - Implement rate limiting

4. **User Experience**
   - Add user authentication
   - Implement user preferences
   - Add content filtering
   - Improve mobile experience

## Best Practices

1. **Go 1.24**
   - Use generics where appropriate
   - Implement proper error handling
   - Use context for cancellation
   - Follow Go idioms and conventions

2. **Security**
   - Use environment variables for sensitive data
   - Implement proper input validation
   - Use prepared statements for SQL
   - Follow OWASP guidelines

3. **Testing**
   - Write unit tests for core logic
   - Implement integration tests
   - Use test fixtures
   - Mock external dependencies

4. **Documentation**
   - Write clear comments
   - Document API endpoints
   - Keep README up to date
   - Document deployment process 