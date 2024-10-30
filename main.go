package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	openai "github.com/sashabaranov/go-openai"
)

func main() {
	s := plexgo.New(
		plexgo.WithSecurity("Vv5Pxp2pWxHXf53E1rhV"),
		plexgo.WithServerURL("http://192.168.1.54:32400"),
	)

	ctx := context.Background()
	res, err := s.Library.GetAllLibraries(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if res.Object != nil {
		for _, l := range res.Object.MediaContainer.Directory {

			key, err := strconv.Atoi(l.Key)
			if err != nil {
				log.Fatal(err)
			}

			items, err := s.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
				Tag:          "all",
				SectionKey:   key,
				Type:         operations.GetLibraryItemsQueryParamTypeMovie.ToPointer(),
				IncludeMeta:  operations.GetLibraryItemsQueryParamIncludeMetaEnable.ToPointer(),
				IncludeGuids: operations.IncludeGuidsEnable.ToPointer(),
			})
			if err != nil {
				log.Fatal(err)
			}
			for _, v := range items.Object.MediaContainer.Metadata {
				js, err := v.MarshalJSON()
				if err != nil {
					log.Fatal(err)
				}

				fmt.Printf("%s\n", js)
			}
		}
	}
}

func GenerateTags(ctx context.Context, text string) ([]string, error) {
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: fmt.Sprintf("given the journal entry %q, generate a few options of single words to summarize the content. Output should be a comma seperated list.", text),
		},
	}

	req := openai.ChatCompletionRequest{
		Model:    openai.GPT4oMini20240718,
		Messages: messages,
	}

	var tags []string
	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}

	for _, choice := range resp.Choices {
		outText := choice.Message.Content
		newTags := strings.Split(outText, ",")
		for _, tag := range newTags {
			tags = append(tags, strings.TrimSpace(tag))
		}
	}
	log.Printf("tags: %+v", tags)

	return nil, nil
}
